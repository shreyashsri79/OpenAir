package daemon

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/shreyashsri79/openair/internal/caps/input"
	"github.com/shreyashsri79/openair/internal/identity"
	"github.com/shreyashsri79/openair/internal/ipc"
	"github.com/shreyashsri79/openair/internal/session"
	openairv1 "github.com/shreyashsri79/openair/internal/wire/openair/v1"
)

// M14 through the daemon (§13, PRD R25).
//
// Both directions live here because both need the session, and one of them
// needs the privilege key: driving another machine is Owned, so the announce
// that opens the exchange carries a proof the daemon signs (D-20, D-82).
//
// The sending side keeps a control session open between requests rather than
// announcing per keystroke. §6.3's announcement is what raises the indicator on
// the machine being driven, and re-announcing per event would flash it on and
// off; it is also an Ed25519 signature each time. So the first request opens
// one, subsequent requests reuse it, and it lapses when nothing has used it for
// a couple of minutes — which is also what lowers the indicator on the far end.

// inputIdle is how long this device keeps a control session open with nothing
// sent through it. Shorter than the far end's own timeout, so the initiator is
// normally the one that ends it and the far end's timeout stays a backstop.
const inputIdle = 90 * time.Second

// controlSession is one live outgoing §6.3 session.
//
// It holds the *peer*, not a session object. A daemon replaces a session when
// the same device connects again -- a reconnection, or the inbound half of two
// dials that crossed -- and events sent on the superseded one fail. What is
// announced is a session with a device, so that is what this remembers, and the
// controller is built against whatever connection currently reaches it.
type controlSession struct {
	id     string
	peer   identity.DeviceID
	killed <-chan struct{}

	mu   sync.Mutex
	used time.Time
}

// onInputEvent is the indicator half of §6.3: something moved on this machine
// because a peer said so, and the person in front of it should be able to find
// out.
//
// Events arrive hundreds a second, so this does not raise one daemon event per
// event -- it refreshes the announcement's activity, and the announcement is
// what a watching shell sees.
func (d *Daemon) onInputEvent(peer identity.DeviceID, _ input.Event) {
	d.mu.Lock()
	for _, a := range d.announcements {
		if a.peer.DeviceID == peer {
			a.touch()
		}
	}
	d.mu.Unlock()
}

// onInput answers a shell's input request.
func (d *Daemon) onInput(ctx context.Context, c *client, payload []byte) {
	var req openairv1.DaemonInputRequest
	if !unmarshal(c, payload, &req) {
		return
	}
	reply := &openairv1.DaemonInputResponse{RequestId: req.GetRequestId()}

	sent, err := d.sendInput(ctx, &req)
	reply.Sent = uint32(sent)
	if err != nil {
		reply.Error = err.Error()
	}
	_ = c.peer.Send(ipc.MsgInputResponse, reply)
}

// sendInput performs one shell request against a device.
func (d *Daemon) sendInput(ctx context.Context, req *openairv1.DaemonInputRequest) (int, error) {
	if req.GetStop() {
		return 0, d.stopControlling(ctx, req.GetDevice())
	}

	cs, sess, err := d.controlling(ctx, req.GetDevice())
	if err != nil {
		return 0, err
	}

	select {
	case <-cs.killed:
		d.dropControl(cs.peer)
		return 0, errors.New("that device stopped the session")
	default:
	}

	ctl := input.NewController(sess)
	sent := 0
	for _, action := range req.GetActions() {
		n, err := applyAction(ctx, ctl, action)
		sent += n
		if err != nil {
			return sent, err
		}
	}
	cs.mu.Lock()
	cs.used = time.Now()
	cs.mu.Unlock()
	return sent, nil
}

// applyAction turns one wire action into events (§13).
func applyAction(ctx context.Context, ctl *input.Controller, a *openairv1.InputAction) (int, error) {
	switch {
	case a.GetText() != "":
		if err := ctl.Type(ctx, a.GetText()); err != nil {
			return 0, err
		}
		// Two events per character, plus shift where it was needed. The exact
		// number is not interesting; that something was sent is.
		return len([]rune(a.GetText())), nil

	case a.GetKey() != "":
		usage, shift, ok := input.Usage(a.GetKey())
		if !ok {
			return 0, fmt.Errorf("unknown key %q", a.GetKey())
		}
		mods, err := input.Modifiers(a.GetModifiers())
		if err != nil {
			return 0, err
		}
		if shift {
			mods |= input.ModLeftShift
		}
		if mods != 0 {
			return 1, ctl.Chord(usage, mods)
		}
		return 1, ctl.Tap(usage, 0)

	case a.GetMove() != nil:
		m := a.GetMove()
		return 1, ctl.Move(m.GetX(), m.GetY(), m.GetAbsolute())

	case a.GetClick() != "":
		button, ok := buttonNamed(a.GetClick())
		if !ok {
			return 0, fmt.Errorf("unknown button %q", a.GetClick())
		}
		return 1, ctl.Click(button)

	case a.GetScroll() != nil:
		s := a.GetScroll()
		return 1, ctl.Scroll(s.GetDx(), s.GetDy(), s.GetPrecise())
	}
	return 0, errors.New("an input action with nothing in it")
}

func buttonNamed(name string) (byte, bool) {
	switch strings.ToLower(name) {
	case "left":
		return input.ButtonLeft, true
	case "right":
		return input.ButtonRight, true
	case "middle":
		return input.ButtonMiddle, true
	case "back":
		return input.ButtonBack, true
	case "forward":
		return input.ButtonForward, true
	}
	return 0, false
}

// controlling returns the live control session for a device, opening one (and
// announcing it, §6.3) if there is none.
func (d *Daemon) controlling(ctx context.Context, target string) (*controlSession, session.Session, error) {
	// A target a shell typed is usually an address, and resolving an address
	// means dialling -- which supersedes the session already open to that
	// device and throws away the control session with it. So the device a
	// target resolved to is remembered, and a second `openair input` to the
	// same address reuses what is already there.
	d.mu.Lock()
	known, remembered := d.controlTargets[target]
	d.mu.Unlock()
	if remembered {
		if sess, live := d.sessionFor(known); live {
			d.mu.Lock()
			cs := d.controls[known]
			d.mu.Unlock()
			if cs != nil {
				return cs, sess, nil
			}
		}
	}

	sess, err := d.sessionTo(ctx, target)
	if err != nil {
		return nil, nil, err
	}
	peer := sess.Peer().DeviceID

	d.mu.Lock()
	if d.controlTargets == nil {
		d.controlTargets = map[string]identity.DeviceID{}
	}
	d.controlTargets[target] = peer
	cs := d.controls[peer]
	d.mu.Unlock()
	if cs != nil {
		return cs, sess, nil
	}

	id, killed, err := d.announce(ctx, sess, []byte{input.CapID}, "keyboard and mouse")
	if err != nil {
		return nil, nil, err
	}
	cs = &controlSession{
		id:     id,
		peer:   peer,
		killed: killed,
		used:   time.Now(),
	}

	d.mu.Lock()
	if d.controls == nil {
		d.controls = map[identity.DeviceID]*controlSession{}
	}
	d.controls[peer] = cs
	d.mu.Unlock()

	d.cfg.Logf("controlling %s", peer.Fingerprint())
	go d.watchControl(cs)
	return cs, sess, nil
}

// watchControl ends a control session that has gone quiet, that the far end
// killed, or whose peer is no longer connected.
//
// Ending it matters on the other machine rather than this one: while it stands,
// that device shows an indicator saying someone is driving it, and accepts
// input datagrams. Leaving one open because a shell exited would leave both
// true indefinitely.
//
// It follows the *peer* rather than the session object it was opened on. A
// daemon replaces a session when the same peer arrives again -- a reconnection,
// or the inbound side of a dial that crossed -- and the control session is with
// the device, not with one QUIC connection to it.
func (d *Daemon) watchControl(cs *controlSession) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-cs.killed:
			d.cfg.Logf("%s ended the control session", cs.peer.Fingerprint())
			d.dropControl(cs.peer)
			return
		case <-ticker.C:
			sess, connected := d.sessionFor(cs.peer)
			if !connected {
				d.dropControl(cs.peer)
				return
			}
			cs.mu.Lock()
			idle := time.Since(cs.used)
			cs.mu.Unlock()
			if idle < inputIdle {
				continue
			}
			d.mu.Lock()
			live := d.controls[cs.peer] == cs
			if live {
				delete(d.controls, cs.peer)
			}
			d.mu.Unlock()
			if live {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				d.endAnnounced(ctx, sess, cs.id, "finished")
				cancel()
				d.cfg.Logf("control session with %s went quiet", cs.peer.Fingerprint())
			}
			return
		}
	}
}

// dropControl forgets a control session without telling the far end, which is
// what a kill from that end means.
func (d *Daemon) dropControl(peer identity.DeviceID) {
	d.mu.Lock()
	delete(d.controls, peer)
	d.mu.Unlock()
}

// stopControlling ends the control session with a device (§6.3's SessionEnd).
func (d *Daemon) stopControlling(ctx context.Context, target string) error {
	sess, err := d.sessionTo(ctx, target)
	if err != nil {
		return err
	}
	peer := sess.Peer().DeviceID

	d.mu.Lock()
	cs := d.controls[peer]
	delete(d.controls, peer)
	d.mu.Unlock()
	if cs == nil {
		return fmt.Errorf("not controlling %s", peer.Fingerprint())
	}
	d.endAnnounced(ctx, sess, cs.id, "finished")
	d.cfg.Logf("stopped controlling %s", peer.Fingerprint())
	return nil
}

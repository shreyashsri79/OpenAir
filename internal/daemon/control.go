package daemon

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/shreyashsri79/openair/internal/caps/input"
	"github.com/shreyashsri79/openair/internal/caps/mirror"
	"github.com/shreyashsri79/openair/internal/identity"
	"github.com/shreyashsri79/openair/internal/session"
	openairv1 "github.com/shreyashsri79/openair/internal/wire/openair/v1"
)

// §6.3's session lifecycle, which M14 is the first capability to need.
//
// The rule §6.3 states is that an initiator announces before it uses `input` or
// `mirror`, and the accessed device shows an indicator for as long as the
// announcement stands and logs the whole thing. M14 leans on it for something
// further: it is where input's Owned proof lives.
//
// That falls out of §13's own shape. Input events are datagrams with no room
// for an `AuthProof` and no msgType to bind one to, and a proof per pointer
// move would be an Ed25519 signature per event on both sides. So the announce
// carries the proof — it is an ordinary control message, so §6's existing gate
// verifies it — and the events that follow are accepted only while that
// announcement is live and the peer is still recorded as Owned, which is
// re-read per datagram (D-82, D-74). A revocation therefore stops input at the
// next event rather than at the next announcement.
//
// The announcement is not a capability: §6.3 puts these on the control stream
// (capID 0), where pairing and punch signalling already live.

const (
	// controlSessionIdle is how long an announced session survives with no
	// events. §6.3 has no timeout of its own, but an announcement that outlives
	// the program that made it is an indicator that never goes away and an
	// authorisation nobody withdrew.
	controlSessionIdle = 2 * time.Minute

	// announceWait bounds how long an initiator waits for §6.3's
	// acknowledgement before giving up (D-83).
	announceWait = 10 * time.Second

	// killCooldown is how long a peer whose session a local user killed is
	// refused a new one. Without it, "kill" means "flicker": the peer announces
	// again a moment later and the person who stopped it watches it come back.
	// It is a cooldown rather than a ban because the peer is still Owned and
	// still unlocked -- what the local user withdrew was this session, not the
	// relationship (§6.3, PRD R4).
	killCooldown = 5 * time.Minute
)

// announced is one live §6.3 session, as the accessed device sees it.
type announced struct {
	id      string
	peer    identity.Peer
	capIDs  []byte
	purpose string
	owned   bool
	started time.Time

	mu   sync.Mutex
	seen time.Time
}

// touch records activity, which is what keeps the announcement alive.
func (a *announced) touch() {
	a.mu.Lock()
	a.seen = time.Now()
	a.mu.Unlock()
}

func (a *announced) idle() time.Duration {
	a.mu.Lock()
	defer a.mu.Unlock()
	return time.Since(a.seen)
}

// onSessionAnnounce records an announcement and raises the indicator (§6.3).
func (d *Daemon) onSessionAnnounce(sess session.Session, payload []byte) error {
	var msg openairv1.SessionAnnounce
	if err := proto.Unmarshal(payload, &msg); err != nil {
		return err
	}
	if msg.GetSessionId() == "" {
		return fmt.Errorf("session announce with no id")
	}

	peer := sess.Peer()
	caps := make([]byte, 0, len(msg.GetCapIds()))
	for _, c := range msg.GetCapIds() {
		// An unknown capability id is dropped rather than refused: a later
		// version announcing something this build has never heard of is
		// announcing more than it will get, not lying (§3.1).
		if wire, ok := session.CapIDToWire(c); ok {
			caps = append(caps, wire)
		}
	}

	a := &announced{
		id:      msg.GetSessionId(),
		peer:    peer,
		capIDs:  caps,
		purpose: msg.GetPurpose(),
		owned:   msg.GetOwnedLevel(),
		started: time.Now(),
		seen:    time.Now(),
	}

	if accepted, reason := d.acceptsAnnounced(peer.DeviceID, caps); !accepted {
		d.cfg.Logf("refused a session from %s: %s", peer.DeviceID.Fingerprint(), reason)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return sess.Send(ctx, 0,
			uint16(openairv1.ControlMessageType_CONTROL_MESSAGE_TYPE_SESSION_ANNOUNCE_ACK),
			&openairv1.SessionAnnounceAck{SessionId: a.id, Accepted: false, Reason: reason})
	}

	d.mu.Lock()
	if d.announcements == nil {
		d.announcements = map[string]*announced{}
	}
	d.announcements[a.id] = a
	d.mu.Unlock()

	d.cfg.Logf("%s announced a session for %s%s", peer.DeviceID.Fingerprint(), capNames(caps), purposeSuffix(a.purpose))
	d.broadcast(&openairv1.DaemonEvent{
		Kind:     openairv1.DaemonEventKind_DAEMON_EVENT_KIND_SESSION_ANNOUNCED,
		DeviceId: string(peer.DeviceID),
		Text:     sessionSummary(a),
	})
	d.logAuth("session-announce", peer.DeviceID, a.purpose)
	go d.watchAnnouncement(a)

	// The acknowledgement is what lets the initiator start (D-83). Sent after
	// the announcement is recorded, so an event arriving the instant it is
	// received already has somewhere to be authorised against.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = sess.Send(ctx, 0,
		uint16(openairv1.ControlMessageType_CONTROL_MESSAGE_TYPE_SESSION_ANNOUNCE_ACK),
		&openairv1.SessionAnnounceAck{SessionId: a.id, Accepted: true})
	return nil
}

// acceptsAnnounced says whether this device will honour what a peer announced,
// and why not when it will not.
//
// The refusal that matters is input: a device with --accept-input off would
// otherwise discard every datagram in silence, and the person driving would
// have no way to tell that from a network fault.
func (d *Daemon) acceptsAnnounced(peer identity.DeviceID, caps []byte) (bool, string) {
	d.mu.Lock()
	until, killed := d.killedUntil[peer]
	d.mu.Unlock()
	if killed && time.Now().Before(until) {
		return false, fmt.Sprintf("someone on that device stopped the session; it will accept another in %s",
			time.Until(until).Round(time.Second))
	}
	for _, c := range caps {
		if c == input.CapID && !d.cfg.AcceptInput {
			return false, "that device is not accepting remote input (its daemon was started without --accept-input)"
		}
		if c == mirror.CapID && !d.cfg.ShareScreen {
			return false, "that device is not sharing its screen (its daemon was started without --share-screen)"
		}
	}
	return true, ""
}

// onSessionAnnounceAck completes an announcement this device made.
func (d *Daemon) onSessionAnnounceAck(payload []byte) error {
	var msg openairv1.SessionAnnounceAck
	if err := proto.Unmarshal(payload, &msg); err != nil {
		return err
	}
	d.mu.Lock()
	ch := d.announceAcks[msg.GetSessionId()]
	d.mu.Unlock()
	if ch == nil {
		return nil
	}
	select {
	case ch <- &msg:
	default:
	}
	return nil
}

// onSessionEnd clears an announcement the peer withdrew.
func (d *Daemon) onSessionEnd(sess session.Session, payload []byte) error {
	var msg openairv1.SessionEnd
	if err := proto.Unmarshal(payload, &msg); err != nil {
		return err
	}
	d.endAnnouncement(msg.GetSessionId(), sess.Peer().DeviceID, msg.GetReason())
	return nil
}

// onSessionKill is the far end telling us a local user stopped our session.
// §6.3 calls it a courtesy; what it does here is stop sending.
func (d *Daemon) onSessionKill(sess session.Session, payload []byte) error {
	var msg openairv1.SessionKill
	if err := proto.Unmarshal(payload, &msg); err != nil {
		return err
	}
	d.cfg.Logf("%s stopped the session we announced (%s)", sess.Peer().DeviceID.Fingerprint(), msg.GetSessionId())
	d.mu.Lock()
	ch := d.outgoingKills[msg.GetSessionId()]
	d.mu.Unlock()
	if ch != nil {
		close(ch)
	}
	return nil
}

// endAnnouncement drops an announcement, releases anything it was holding, and
// lowers the indicator.
func (d *Daemon) endAnnouncement(id string, peer identity.DeviceID, reason string) {
	d.mu.Lock()
	a := d.announcements[id]
	delete(d.announcements, id)
	d.mu.Unlock()
	if a == nil {
		return
	}

	// Anything the peer was holding down goes up now, not in five seconds:
	// a session that ended is not a network hiccup (§13's safety release is
	// for those).
	if d.input != nil {
		d.input.Forget(a.peer.DeviceID)
	}
	// And a screen stops being shared. §6.3 says the enforcement of a kill is
	// local refusal, so ending the announcement has to stop the capture as
	// well as the authorisation -- otherwise "stop watching me" leaves an
	// encoder running and frames flowing.
	if d.mirrors != nil {
		d.mirrors.StopSharingWith(a.peer.DeviceID)
	}
	if reason == "" {
		reason = "ended"
	}
	d.cfg.Logf("session with %s %s", peer.Fingerprint(), reason)
	d.broadcast(&openairv1.DaemonEvent{
		Kind:     openairv1.DaemonEventKind_DAEMON_EVENT_KIND_SESSION_ENDED,
		DeviceId: string(a.peer.DeviceID),
		Text:     reason,
	})
	d.logAuth("session-end", peer, reason)
}

// watchAnnouncement drops an announcement that has gone quiet.
func (d *Daemon) watchAnnouncement(a *announced) {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		d.mu.Lock()
		_, live := d.announcements[a.id]
		d.mu.Unlock()
		if !live {
			return
		}
		if a.idle() > controlSessionIdle {
			d.endAnnouncement(a.id, a.peer.DeviceID, "went quiet")
			return
		}
	}
}

// inputAllowed is what the input capability asks before applying an event
// (D-82): is there a live announcement from this peer that named input?
func (d *Daemon) inputAllowed(peer identity.DeviceID) bool {
	if !d.cfg.AcceptInput {
		return false
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, a := range d.announcements {
		if a.peer.DeviceID != peer {
			continue
		}
		if !a.owned {
			continue
		}
		for _, c := range a.capIDs {
			if c == input.CapID {
				a.touch()
				return true
			}
		}
	}
	return false
}

// KillSessions ends every announced session from a peer, or all of them when
// peer is empty, and tells the far end (§6.3).
func (d *Daemon) KillSessions(ctx context.Context, peer identity.DeviceID) int {
	d.mu.Lock()
	var victims []*announced
	for _, a := range d.announcements {
		if peer == "" || a.peer.DeviceID == peer {
			victims = append(victims, a)
		}
	}
	d.mu.Unlock()

	d.mu.Lock()
	if d.killedUntil == nil {
		d.killedUntil = map[identity.DeviceID]time.Time{}
	}
	for _, a := range victims {
		d.killedUntil[a.peer.DeviceID] = time.Now().Add(killCooldown)
	}
	d.mu.Unlock()

	for _, a := range victims {
		d.endAnnouncement(a.id, a.peer.DeviceID, "stopped here")
		if sess, ok := d.sessionFor(a.peer.DeviceID); ok {
			_ = sess.Send(ctx, 0,
				uint16(openairv1.ControlMessageType_CONTROL_MESSAGE_TYPE_SESSION_KILL),
				&openairv1.SessionKill{SessionId: a.id})
		}
	}
	return len(victims)
}

// AnnouncedSessions is what `openair status` reports and what a shell shows as
// the indicator §6.3 requires.
func (d *Daemon) AnnouncedSessions() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]string, 0, len(d.announcements))
	for _, a := range d.announcements {
		out = append(out, sessionSummary(a))
	}
	return out
}

// announce is the initiating half: tell the peer what is about to happen, with
// an Owned proof when this device has one (§6.3, §6).
func (d *Daemon) announce(ctx context.Context, sess session.Session, capIDs []byte, purpose string) (string, <-chan struct{}, error) {
	id, err := sessionID()
	if err != nil {
		return "", nil, err
	}
	wire := make([]openairv1.CapabilityId, 0, len(capIDs))
	for _, c := range capIDs {
		id, ok := session.CapIDFromWire(c)
		if !ok {
			return "", nil, fmt.Errorf("cannot announce unknown capability %d", c)
		}
		wire = append(wire, id)
	}

	msg := &openairv1.SessionAnnounce{
		SessionId:  id,
		CapIds:     wire,
		Purpose:    purpose,
		OwnedLevel: true,
	}
	msgType := uint16(openairv1.ControlMessageType_CONTROL_MESSAGE_TYPE_SESSION_ANNOUNCE)

	// The proof rides here and nowhere else: what follows is datagrams, which
	// have no room for one (D-82).
	proven, err := session.SendOwnedIfUnlocked(ctx, sess, 0, msgType, msg)
	if err != nil {
		return "", nil, err
	}
	if !proven {
		return "", nil, fmt.Errorf("controlling a device is Owned-level: run `openair unlock %s` first",
			sess.Peer().DeviceID.Fingerprint())
	}

	killed := make(chan struct{})
	acks := make(chan *openairv1.SessionAnnounceAck, 1)
	d.mu.Lock()
	if d.outgoingKills == nil {
		d.outgoingKills = map[string]chan struct{}{}
	}
	if d.announceAcks == nil {
		d.announceAcks = map[string]chan *openairv1.SessionAnnounceAck{}
	}
	d.outgoingKills[id] = killed
	d.announceAcks[id] = acks
	d.mu.Unlock()

	defer func() {
		d.mu.Lock()
		delete(d.announceAcks, id)
		d.mu.Unlock()
	}()

	// Waiting here is the whole point of the acknowledgement: input events are
	// datagrams and have no ordering against the control stream, so events sent
	// before the far end has processed the announce are discarded as
	// unauthorised (D-83).
	select {
	case ack := <-acks:
		if !ack.GetAccepted() {
			d.mu.Lock()
			delete(d.outgoingKills, id)
			d.mu.Unlock()
			reason := ack.GetReason()
			if reason == "" {
				reason = "that device refused the session"
			}
			return "", nil, errors.New(reason)
		}
	case <-ctx.Done():
		return "", nil, fmt.Errorf("that device did not answer the session announcement: %w", ctx.Err())
	case <-time.After(announceWait):
		return "", nil, errors.New("that device did not answer the session announcement")
	}
	return id, killed, nil
}

// endAnnounced tells the peer we are finished (§6.3).
func (d *Daemon) endAnnounced(ctx context.Context, sess session.Session, id, reason string) {
	d.mu.Lock()
	delete(d.outgoingKills, id)
	d.mu.Unlock()

	_ = sess.Send(ctx, 0,
		uint16(openairv1.ControlMessageType_CONTROL_MESSAGE_TYPE_SESSION_END),
		&openairv1.SessionEnd{SessionId: id, Reason: reason})
}

func sessionSummary(a *announced) string {
	name := a.peer.DisplayName
	if name == "" {
		name = a.peer.DeviceID.Fingerprint()
	}
	s := fmt.Sprintf("%s is using %s", name, capNames(a.capIDs))
	if a.purpose != "" {
		s += ": " + a.purpose
	}
	return s
}

func purposeSuffix(purpose string) string {
	if purpose == "" {
		return ""
	}
	return ": " + purpose
}

// capNames turns wire capIDs into something a person reads on an indicator.
// §6.2 insists a consent prompt shows the operation verbatim; the same applies
// to an indicator that is the only sign someone is driving this machine.
func capNames(caps []byte) string {
	if len(caps) == 0 {
		return "this device"
	}
	names := make([]string, 0, len(caps))
	for _, c := range caps {
		switch c {
		case 1:
			names = append(names, "file transfer")
		case 2:
			names = append(names, "clipboard")
		case 3:
			names = append(names, "your files")
		case 4:
			names = append(names, "notifications")
		case input.CapID:
			names = append(names, "keyboard and mouse")
		case 6:
			names = append(names, "screen")
		default:
			names = append(names, fmt.Sprintf("capability %d", c))
		}
	}
	return strings.Join(names, " and ")
}

// sessionID is §6.3's opaque session identifier.
func sessionID() (string, error) {
	b := make([]byte, 10)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b)), nil
}

package daemon

import (
	"context"
	"errors"
	"fmt"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/shreyashsri79/openair/internal/identity"
	"github.com/shreyashsri79/openair/internal/path"
	"github.com/shreyashsri79/openair/internal/rendezvous"
	"github.com/shreyashsri79/openair/internal/session"
	openairv1 "github.com/shreyashsri79/openair/internal/wire/openair/v1"
)

// Punching and migration, M9 (§18).
//
// A session that came up over the relay is usable immediately, which is why
// §18 starts there — but it costs an extra hop and it tells the relay operator
// that these two devices are talking. So once the session exists, the two ends
// use it to tell each other where they might be reachable directly, spray
// probes at each other's candidates, and move the traffic across if a pair
// works.
//
// The move itself is invisible: internal/path switches the route underneath
// QUIC, so the connection ID, the streams and any transfer in flight carry on
// (PRD R9). If the punch fails — symmetric NAT on either side is enough — the
// session simply stays on the relay, which is why failing is cheap enough to
// retry.

const (
	// punchLead is how far ahead the start_at hint is set. Long enough that a
	// peer on a slow link has the message before the instant it names, short
	// enough not to delay a punch that could already have started.
	punchLead = 300 * time.Millisecond

	// readyWait bounds how long the initiator waits for PunchReady. A peer that
	// does not answer is either an older build or one that has decided not to
	// punch; either way there is nothing to wait for.
	readyWait = 10 * time.Second

	// upgradeRetry is how often a relayed session tries again. §18 asks for a
	// re-race on network change; without an event for that, a slow retry covers
	// the same ground — the usual reason a punch starts working is that one
	// side moved network.
	upgradeRetry = 60 * time.Second

	// candidateWait bounds gathering local candidates, which includes asking a
	// STUN server.
	candidateWait = 5 * time.Second
)

// controlHandler is capID 0.
//
// §18's punch messages travel over an existing session, and capID 0 is where
// session-level messages live (Appendix B), so this multiplexes them with
// pairing and revocation rather than inventing a capability for two messages
// that are not a capability.
type controlHandler struct{ d *Daemon }

func (h *controlHandler) CapID() byte { return 0 }

func (h *controlHandler) Serve(ctx context.Context, sess session.Session, msgType uint16, payload []byte) error {
	switch openairv1.ControlMessageType(msgType) {
	case openairv1.ControlMessageType_CONTROL_MESSAGE_TYPE_PUNCH_REQUEST:
		return h.d.onPunchRequest(ctx, sess, payload)
	case openairv1.ControlMessageType_CONTROL_MESSAGE_TYPE_PUNCH_READY:
		return h.d.onPunchReady(payload)
	case openairv1.ControlMessageType_CONTROL_MESSAGE_TYPE_SESSION_ANNOUNCE:
		return h.d.onSessionAnnounce(sess, payload)
	case openairv1.ControlMessageType_CONTROL_MESSAGE_TYPE_SESSION_END:
		return h.d.onSessionEnd(sess, payload)
	case openairv1.ControlMessageType_CONTROL_MESSAGE_TYPE_SESSION_KILL:
		return h.d.onSessionKill(sess, payload)
	case openairv1.ControlMessageType_CONTROL_MESSAGE_TYPE_SESSION_ANNOUNCE_ACK:
		return h.d.onSessionAnnounceAck(payload)
	}
	return h.d.pairs.Serve(ctx, sess, msgType, payload)
}

func (h *controlHandler) ServeStream(ctx context.Context, sess session.Session, st session.Stream, msgType uint16, payload []byte) error {
	return h.d.pairs.ServeStream(ctx, sess, st, msgType, payload)
}

// maybeUpgrade starts trying to get a session off the relay.
//
// Only one side signals, chosen by comparing DeviceIDs, because two
// simultaneous exchanges would punch with two tokens towards the same pair of
// addresses and the second would be pure noise. The rule is arbitrary but it
// has to be agreed, and both ends know both IDs.
func (d *Daemon) maybeUpgrade(sess session.Session) {
	if d.paths == nil {
		return
	}
	if !initiatesPunch(d.id.DeviceID(), sess.Peer().DeviceID) {
		return
	}
	go d.upgradeLoop(sess)
}

// initiatesPunch decides which end signals. Comparing DeviceIDs is arbitrary
// and that is fine -- what matters is that both ends compute the same answer
// from values they both already have.
func initiatesPunch(local, peer identity.DeviceID) bool {
	return peer.Valid() && local.Valid() && string(local) < string(peer)
}

// upgradeLoop keeps a relayed session under review until it ends.
func (d *Daemon) upgradeLoop(sess session.Session) {
	peer := sess.Peer().DeviceID
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		select {
		case <-sess.Done():
			cancel()
		case <-ctx.Done():
		}
	}()

	for {
		if d.paths.Class(peer) == path.ClassRelayed && d.paths.CanUpgrade(peer) {
			if err := d.upgrade(ctx, sess); err != nil && ctx.Err() == nil {
				// Expected often enough that it is a log line and not an event:
				// a symmetric NAT on either side makes this the normal outcome,
				// and the session is still working over the relay.
				d.cfg.Logf("no direct path to %s yet: %v", peer.Fingerprint(), err)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(upgradeRetry):
		}
	}
}

// upgrade runs one exchange and one punch, and promotes the winner.
func (d *Daemon) upgrade(ctx context.Context, sess session.Session) error {
	peer := sess.Peer().DeviceID

	token, err := path.NewToken()
	if err != nil {
		return err
	}
	ready := make(chan *openairv1.PunchReady, 1)
	key := string(token)

	d.mu.Lock()
	d.punches[key] = ready
	d.mu.Unlock()
	defer func() {
		d.mu.Lock()
		delete(d.punches, key)
		d.mu.Unlock()
	}()

	startAt := time.Now().Add(punchLead)
	req := &openairv1.PunchRequest{
		TargetDeviceId: string(peer),
		Candidates:     d.localCandidates(ctx),
		PunchToken:     token,
		StartAt:        startAt.UnixMilli(),
	}
	if err := sess.Send(ctx, 0, session.MsgType(openairv1.ControlMessageType_CONTROL_MESSAGE_TYPE_PUNCH_REQUEST), req); err != nil {
		return fmt.Errorf("send punch request: %w", err)
	}

	var answer *openairv1.PunchReady
	select {
	case answer = <-ready:
	case <-time.After(readyWait):
		return errors.New("the peer did not answer the punch request")
	case <-ctx.Done():
		return ctx.Err()
	}

	// The spray is synchronised by this exchange rather than by start_at: see
	// path.SprayDelay for why an absolute instant cannot be trusted (D-67).
	// What is left of start_at is a short "not before", honoured only when the
	// local clock agrees it is in the near future.
	if delay := path.SprayDelay(startAt, time.Now()); delay > 0 {
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return d.punchAndPromote(ctx, peer, token, answer.GetCandidates())
}

// onPunchRequest is the far end of upgrade: answer with this device's
// candidates, then punch towards the peer's.
func (d *Daemon) onPunchRequest(ctx context.Context, sess session.Session, payload []byte) error {
	var req openairv1.PunchRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		return err
	}
	if target := identity.DeviceID(req.GetTargetDeviceId()); target != "" && target != d.id.DeviceID() {
		// The message arrived on a session with this device, so a different
		// target means the peer is confused rather than malicious. Punching
		// anyway would open a mapping for a device that is not asking.
		return fmt.Errorf("punch request addressed to %s, this is %s", target, d.id.DeviceID())
	}
	if len(req.GetPunchToken()) != path.TokenLen {
		return fmt.Errorf("punch token is %d bytes, want %d", len(req.GetPunchToken()), path.TokenLen)
	}

	// Answered off the control loop: Serve must not block, and both the reply
	// and the punch that follows it take a while.
	go d.answerPunch(context.WithoutCancel(ctx), sess, &req)
	return nil
}

// answerPunch sends PunchReady and sprays. Spraying starts here rather than at
// start_at, which is what actually synchronises the two sides (D-67).
func (d *Daemon) answerPunch(ctx context.Context, sess session.Session, req *openairv1.PunchRequest) {
	peer := sess.Peer().DeviceID
	token := req.GetPunchToken()

	reply := &openairv1.PunchReady{
		Candidates: d.localCandidates(ctx),
		PunchToken: token,
	}
	sendCtx, cancel := context.WithTimeout(ctx, readyWait)
	err := sess.Send(sendCtx, 0, session.MsgType(openairv1.ControlMessageType_CONTROL_MESSAGE_TYPE_PUNCH_READY), reply)
	cancel()
	if err != nil {
		d.cfg.Logf("answering %s's punch request: %v", peer.Fingerprint(), err)
		return
	}
	if err := d.punchAndPromote(ctx, peer, token, req.GetCandidates()); err != nil {
		d.cfg.Logf("no direct path to %s yet: %v", peer.Fingerprint(), err)
	}
}

// onPunchReady hands an answer to whoever is waiting on that token.
func (d *Daemon) onPunchReady(payload []byte) error {
	var ready openairv1.PunchReady
	if err := proto.Unmarshal(payload, &ready); err != nil {
		return err
	}
	d.mu.Lock()
	waiter := d.punches[string(ready.GetPunchToken())]
	d.mu.Unlock()
	if waiter == nil {
		// An answer to a punch that has already given up, or a token nobody
		// asked about. Nothing to do, and nothing worth failing the session
		// over.
		return nil
	}
	select {
	case waiter <- &ready:
	default:
	}
	return nil
}

// punchAndPromote races the peer's candidates and, if one answers, moves the
// session's traffic onto it.
func (d *Daemon) punchAndPromote(ctx context.Context, peer identity.DeviceID, token []byte, candidates []string) error {
	punchCtx, cancel := context.WithTimeout(ctx, path.PunchWindow)
	defer cancel()

	addr, err := d.paths.Punch(punchCtx, peer, token, path.ParseCandidates(candidates))
	if err != nil {
		return err
	}
	d.paths.Promote(peer, addr, token)
	d.cfg.Logf("%s is now reached directly at %s; the relay is no longer in the path",
		peer.Fingerprint(), addr)
	return nil
}

// localCandidates is §18 step 1: where this device might be reachable.
//
// The local addresses come from the bound listener, which is the same set
// published to a rendezvous server. The reflexive address comes from STUN, and
// is the one that matters behind NAT — a candidate list of private addresses
// only is useful on a LAN and useless anywhere else.
func (d *Daemon) localCandidates(ctx context.Context) []string {
	var out []string
	if eps, err := rendezvous.EndpointsFor(d.ln.Addr()); err == nil {
		out = append(out, eps...)
	} else {
		d.cfg.Logf("cannot work out this device's local candidates: %v", err)
	}

	if servers := d.stunServers(); len(servers) > 0 {
		stunCtx, cancel := context.WithTimeout(ctx, candidateWait)
		reflexive, err := d.paths.Reflexive(stunCtx, servers)
		cancel()
		if err != nil {
			d.cfg.Logf("no reflexive address: %v", err)
		}
		out = append(out, path.FormatCandidates(reflexive)...)
	}

	seen := make(map[string]struct{}, len(out))
	unique := out[:0]
	for _, c := range out {
		if _, dup := seen[c]; dup {
			continue
		}
		seen[c] = struct{}{}
		unique = append(unique, c)
	}
	return unique
}

// stunServers is what to ask for a reflexive address.
//
// Defaulting to the rendezvous server means self-hosting stays one server:
// openair-rendezvous answers STUN on its own port (D-68). A device with
// neither a configured STUN server nor a rendezvous one punches with its local
// addresses only, which works on a LAN and not through NAT.
func (d *Daemon) stunServers() []string {
	if len(d.cfg.STUN) > 0 {
		return d.cfg.STUN
	}
	if d.cfg.Rendezvous.Addr != "" {
		return []string{d.cfg.Rendezvous.Addr}
	}
	return nil
}

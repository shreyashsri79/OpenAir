package path

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"sync"
	"time"

	"github.com/shreyashsri79/openair/internal/identity"
)

// Hole punching, §18 step 3.
//
// Both sides spray probes at every candidate address the other offered, from
// the same socket their QUIC traffic uses. Each probe crossing a NAT creates an
// outbound mapping, and a probe arriving from the far side while that mapping
// exists is delivered rather than dropped — which is the whole mechanism. It
// works often but not always: a symmetric NAT allocates a different external
// port per destination, so the port the peer was told about is not the one its
// packets will arrive on, and the punch fails. That failure is ordinary, and
// the relay is what makes it survivable rather than fatal.

const (
	// probeInterval is how often each candidate is re-probed during a punch.
	// Fast, because the whole attempt is measured in seconds and a single lost
	// probe should not cost the attempt.
	probeInterval = 100 * time.Millisecond

	// PunchWindow is how long an attempt sprays before giving up. A punch that
	// has not worked in this long is not going to.
	PunchWindow = 4 * time.Second

	// maxStartSkew bounds how far into the future a peer's start_at may be
	// before it is ignored. See SprayDelay.
	maxStartSkew = 2 * time.Second

	// punchLinger is how long a finished attempt keeps answering probes.
	//
	// Without it the winner of the race silently stops answering the moment its
	// own check completes, and the peer — which is one round trip behind, and
	// needs an answer of its own to accept the address — times out against a
	// path that demonstrably works. Both sides have to validate, so the side
	// that finishes first has to stay available for the other.
	punchLinger = 5 * time.Second

	// stunTimeout bounds one STUN server. Servers that do not answer are
	// common; waiting long for them is not useful.
	stunTimeout = 2 * time.Second
)

// ErrPunchFailed reports that no candidate pair became reachable. It is an
// expected outcome, not a broken one: the session carries on over the relay.
var ErrPunchFailed = errors.New("path: no direct path to the peer was found")

// punchState is one attempt in progress, keyed by its token.
type punchState struct {
	peer   identity.DeviceID
	result chan netip.AddrPort

	mu         sync.Mutex
	candidates map[netip.AddrPort]struct{}
	done       bool
}

// NewToken draws §18's 16-byte punch_token.
func NewToken() ([]byte, error) {
	token := make([]byte, TokenLen)
	if _, err := rand.Read(token); err != nil {
		return nil, err
	}
	return token, nil
}

// Punch races the candidate addresses and returns the first one proved
// reachable in both directions.
//
// "Both directions" is the load-bearing part. An address is not accepted
// because a probe arrived from it — a peer could name somebody else's address
// and turn this device into a packet source pointed at a stranger. It is
// accepted when a probe *this* device sent to it comes back answered, which
// requires whoever is there to hold the token that only ever travelled inside
// the encrypted session.
func (c *Conn) Punch(ctx context.Context, peer identity.DeviceID, token []byte, candidates []netip.AddrPort) (netip.AddrPort, error) {
	if len(token) != TokenLen {
		return netip.AddrPort{}, fmt.Errorf("path: punch token is %d bytes, want %d", len(token), TokenLen)
	}
	st := &punchState{
		peer:       peer,
		result:     make(chan netip.AddrPort, 1),
		candidates: map[netip.AddrPort]struct{}{},
	}
	for _, ap := range candidates {
		if ap.IsValid() {
			st.candidates[ap] = struct{}{}
		}
	}

	key := string(token)
	c.mu.Lock()
	c.punches[key] = st
	c.mu.Unlock()
	defer func() {
		time.AfterFunc(punchLinger, func() {
			c.mu.Lock()
			if c.punches[key] == st {
				delete(c.punches, key)
			}
			c.mu.Unlock()
		})
	}()

	ticker := time.NewTicker(probeInterval)
	defer ticker.Stop()

	for {
		// Sprayed before the first tick as well as on every tick: the peer is
		// spraying too, and the sooner a mapping exists the sooner one of its
		// probes gets through.
		for _, ap := range st.snapshot() {
			c.sendProbe(probePing, token, ap)
		}

		select {
		case addr := <-st.result:
			return addr, nil
		case <-ctx.Done():
			return netip.AddrPort{}, fmt.Errorf("%w: %w", ErrPunchFailed, ctx.Err())
		case <-c.closed:
			return netip.AddrPort{}, ErrClosed
		case <-ticker.C:
		}
	}
}

func (st *punchState) snapshot() []netip.AddrPort {
	st.mu.Lock()
	defer st.mu.Unlock()
	out := make([]netip.AddrPort, 0, len(st.candidates))
	for ap := range st.candidates {
		out = append(out, ap)
	}
	return out
}

// add records an address the peer's own probe arrived from. That address is
// the peer's mapping as this side's NAT sees it, which is frequently not one
// either side could have predicted, and probing it back is what completes the
// pair.
func (st *punchState) add(ap netip.AddrPort) {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.candidates[ap] = struct{}{}
}

func (st *punchState) finish(ap netip.AddrPort) {
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.done {
		return
	}
	st.done = true
	st.result <- ap
}

// handleProbe answers or completes one connectivity check.
func (c *Conn) handleProbe(kind probeKind, token []byte, sender identity.DeviceID, src netip.AddrPort) {
	c.mu.Lock()
	st := c.punches[string(token)]
	routedPeer, routed := c.byAddr[src]
	if routed {
		if r := c.routes[routedPeer]; r != nil {
			r.lastRx = time.Now()
		}
	}
	c.mu.Unlock()

	inPunch := st != nil && st.peer == sender
	onRoute := routed && routedPeer == sender

	switch kind {
	case probePing:
		// Answered only for a peer this device is actually punching towards or
		// already routing to. Anything else gets nothing back, so the socket is
		// not a probe reflector for whoever finds it.
		if !inPunch && !onRoute {
			return
		}
		if inPunch {
			st.add(src)
		}
		c.sendProbe(probePong, token, src)
	case probePong:
		if inPunch {
			st.finish(src)
		}
	}
}

// sendProbe writes one probe. Failures are silent: an unreachable candidate is
// the normal case and the punch as a whole reports the outcome.
func (c *Conn) sendProbe(kind probeKind, token []byte, to netip.AddrPort) {
	b, err := encodeProbe(kind, token, c.local)
	if err != nil {
		return
	}
	_, _ = c.udp.WriteTo(b, net.UDPAddrFromAddrPort(to))
}

// SprayDelay says how long to wait before spraying, given a peer's start_at.
//
// §18 requires both sides to begin within about 50 ms of start_at and, in the
// same paragraph, forbids relying on clocks being that good. Those cannot both
// be met by reading start_at as an absolute instant: two devices whose clocks
// differ by a minute — which is ordinary, and is what NTP exists to fix — would
// spray a minute apart, and the field would have made traversal worse rather
// than better (D-67).
//
// So the exchange itself is the synchronisation: the responder sprays when it
// sends PunchReady and the initiator sprays when it receives one, which puts
// them within one one-way delay of each other with no clock involved. start_at
// is honoured only as a short "not before" hint, and ignored entirely when it
// disagrees with the local clock by more than a couple of seconds, because at
// that point it is measuring skew rather than intent.
func SprayDelay(startAt, now time.Time) time.Duration {
	if startAt.IsZero() {
		return 0
	}
	d := startAt.Sub(now)
	if d <= 0 || d > maxStartSkew {
		return 0
	}
	return d
}

// --- reflexive addresses -----------------------------------------------------

// Reflexive asks STUN servers what address this socket appears to come from
// (§18 step 1).
//
// Every server is asked, and every distinct answer is kept: two different
// answers mean the NAT allocates a mapping per destination, so neither address
// will be the one the peer's packets arrive on. Keeping both is still right —
// the punch costs little and the alternative is not trying.
func (c *Conn) Reflexive(ctx context.Context, servers []string) ([]netip.AddrPort, error) {
	var (
		out  []netip.AddrPort
		seen = map[netip.AddrPort]struct{}{}
		errs []error
	)
	for _, server := range servers {
		ap, err := c.reflexiveFrom(ctx, server)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if _, dup := seen[ap]; dup {
			continue
		}
		seen[ap] = struct{}{}
		out = append(out, ap)
	}
	if len(out) == 0 && len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return out, nil
}

// reflexiveFrom runs one Binding transaction.
func (c *Conn) reflexiveFrom(ctx context.Context, server string) (netip.AddrPort, error) {
	addr, err := net.ResolveUDPAddr("udp", server)
	if err != nil {
		return netip.AddrPort{}, fmt.Errorf("path: STUN server %q: %w", server, err)
	}
	id, err := newTxID()
	if err != nil {
		return netip.AddrPort{}, err
	}

	reply := make(chan netip.AddrPort, 1)
	c.mu.Lock()
	c.stun[id] = reply
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.stun, id)
		c.mu.Unlock()
	}()

	ctx, cancel := context.WithTimeout(ctx, stunTimeout)
	defer cancel()

	req := bindingRequest(id)
	ticker := time.NewTicker(300 * time.Millisecond)
	defer ticker.Stop()

	for {
		if _, err := c.udp.WriteTo(req, addr); err != nil {
			return netip.AddrPort{}, fmt.Errorf("path: STUN request to %s: %w", server, err)
		}
		select {
		case ap := <-reply:
			return ap, nil
		case <-ctx.Done():
			return netip.AddrPort{}, fmt.Errorf("path: STUN server %s did not answer: %w", server, ctx.Err())
		case <-c.closed:
			return netip.AddrPort{}, ErrClosed
		case <-ticker.C:
		}
	}
}

// handleSTUN routes a Binding response to whoever is waiting for it.
func (c *Conn) handleSTUN(b []byte) {
	typ, id, mapped, err := parseSTUN(b)
	if err != nil || typ != stunBindingResponse || !mapped.IsValid() {
		return
	}
	c.mu.Lock()
	reply := c.stun[id]
	c.mu.Unlock()
	if reply == nil {
		return
	}
	select {
	case reply <- mapped:
	default:
	}
}

// --- candidates --------------------------------------------------------------

// ParseCandidates reads §18's candidate list, which is "ip:port" strings on the
// wire. Anything unparseable is skipped rather than fatal: a peer offering one
// bad candidate among several should cost that candidate, not the attempt.
func ParseCandidates(list []string) []netip.AddrPort {
	out := make([]netip.AddrPort, 0, len(list))
	for _, s := range list {
		ap, err := netip.ParseAddrPort(s)
		if err != nil {
			continue
		}
		if !ap.IsValid() || ap.Port() == 0 {
			continue
		}
		out = append(out, netip.AddrPortFrom(ap.Addr().Unmap(), ap.Port()))
	}
	return out
}

// FormatCandidates renders candidates for the wire.
func FormatCandidates(list []netip.AddrPort) []string {
	out := make([]string, 0, len(list))
	for _, ap := range list {
		out = append(out, ap.String())
	}
	return out
}

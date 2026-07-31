package path

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"sync"
	"time"

	"github.com/shreyashsri79/openair/internal/identity"
	"github.com/shreyashsri79/openair/internal/relay"
)

// ErrClosed reports use of a Conn whose socket has been closed.
var ErrClosed = errors.New("path: connection closed")

const (
	// inboundQueue bounds packets read off the wire and not yet taken by the
	// QUIC stack. These are QUIC packets: dropping one is a case QUIC already
	// handles, and buffering without limit is not.
	inboundQueue = 256

	// readBuffer is one datagram. A QUIC packet is at most a path MTU; the
	// slack is for a relay or a jumbo-framed LAN.
	readBuffer = 4 << 10

	// keepaliveInterval is how often a quiet direct route is probed. NAT
	// mappings commonly expire after 30 seconds of silence, so a route that
	// carries no traffic still has to be poked or it stops existing.
	keepaliveInterval = 15 * time.Second

	// keepaliveTimeout is how long a direct route may receive nothing at all
	// before it is abandoned for the relay. QUIC's own keepalive runs every
	// five seconds, so a live path is never this quiet — reaching it means the
	// path is gone, not idle.
	keepaliveTimeout = 45 * time.Second

	// repromoteBackoff is §18's hysteresis, in its simplest useful form: a peer
	// whose direct path has just been abandoned is not upgraded again
	// immediately. Without it a marginal path that dies every few seconds is
	// re-punched every few seconds, and the session spends its life migrating
	// instead of moving data.
	repromoteBackoff = 30 * time.Second
)

// Config is what New needs.
type Config struct {
	// Local is this device's DeviceID, which probes carry so that a peer can
	// tell who is punching towards it.
	Local identity.DeviceID

	// UDP is the socket everything shares: QUIC, STUN and punch probes. It is
	// required, and it is the caller's to close — Conn.Close closes it too,
	// because the QUIC stack above expects closing its packet conn to free the
	// socket.
	UDP net.PacketConn

	Logf func(format string, args ...any)
}

// Conn is a net.PacketConn that reaches peers over whichever path currently
// works, and switches between them without the QUIC connection above noticing.
//
// See the package comment for why the switch happens here rather than through
// quic-go's own migration.
type Conn struct {
	local identity.DeviceID
	udp   net.PacketConn
	logf  func(string, ...any)

	in     chan inbound
	closed chan struct{}
	once   sync.Once

	mu       sync.Mutex
	relay    *relay.PacketConn
	routes   map[identity.DeviceID]*route
	byAddr   map[netip.AddrPort]identity.DeviceID
	viaID    map[identity.DeviceID]bool
	cooldown map[identity.DeviceID]time.Time
	punches  map[string]*punchState
	stun     map[txID]chan netip.AddrPort

	dmu           sync.Mutex
	readDeadline  time.Time
	deadlineTimer *time.Timer
	deadlineCh    chan struct{}
}

// route is one validated direct path to a peer.
type route struct {
	addr   netip.AddrPort
	token  []byte
	since  time.Time
	lastRx time.Time
}

type inbound struct {
	payload []byte
	from    net.Addr
}

// New wraps a UDP socket. Nothing is relayed until SetRelay is called, and no
// peer has a direct route until a punch validates one.
func New(cfg Config) (*Conn, error) {
	if cfg.UDP == nil {
		return nil, errors.New("path: Config.UDP is required")
	}
	if !cfg.Local.Valid() {
		return nil, fmt.Errorf("path: Config.Local %q is not a device id", cfg.Local)
	}
	if cfg.Logf == nil {
		cfg.Logf = func(string, ...any) {}
	}
	c := &Conn{
		local:      cfg.Local,
		udp:        cfg.UDP,
		logf:       cfg.Logf,
		in:         make(chan inbound, inboundQueue),
		closed:     make(chan struct{}),
		routes:     map[identity.DeviceID]*route{},
		byAddr:     map[netip.AddrPort]identity.DeviceID{},
		viaID:      map[identity.DeviceID]bool{},
		cooldown:   map[identity.DeviceID]time.Time{},
		punches:    map[string]*punchState{},
		stun:       map[txID]chan netip.AddrPort{},
		deadlineCh: make(chan struct{}),
	}
	go c.readUDP()
	go c.watchRoutes()
	return c, nil
}

// SetRelay attaches (or, with nil, detaches) the relay this device is reachable
// through. Packets for a peer with no direct route travel over it.
//
// The relay connection remains the caller's to close; M8's reconnect loop owns
// its lifetime and hands each new one here.
func (c *Conn) SetRelay(pc *relay.PacketConn) {
	c.mu.Lock()
	previous := c.relay
	c.relay = pc
	c.mu.Unlock()

	if previous == pc || pc == nil {
		return
	}
	go c.readRelay(pc)
}

// Relay reports the attached relay connection, or nil.
func (c *Conn) Relay() *relay.PacketConn {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.relay
}

// readUDP is the socket's read loop. It runs whether or not anybody is in
// ReadFrom, because probes and STUN responses have to be handled even while the
// QUIC stack is busy elsewhere.
func (c *Conn) readUDP() {
	defer c.Close()
	for {
		buf := make([]byte, readBuffer)
		n, from, err := c.udp.ReadFrom(buf)
		if err != nil {
			select {
			case <-c.closed:
				return
			default:
			}
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				continue
			}
			return
		}
		c.handleUDP(buf[:n], from)
	}
}

// handleUDP demultiplexes one datagram off the shared socket.
func (c *Conn) handleUDP(b []byte, from net.Addr) {
	src, ok := addrPortOf(from)
	if !ok {
		return
	}

	if !isQUIC(b) {
		if isSTUN(b) {
			c.handleSTUN(b)
			return
		}
		if kind, token, sender, err := decodeProbe(b); err == nil {
			c.handleProbe(kind, token, sender, src)
			return
		}
		// Neither ours nor QUIC's. Dropping is the only safe answer: this
		// socket is reachable by anyone.
		return
	}

	// A QUIC packet from an address a punch validated belongs to that peer's
	// connection, and must be reported under the peer's stable address rather
	// than the ip:port it happened to arrive from — that identity is what makes
	// the migration invisible above.
	c.mu.Lock()
	peer, routed := c.byAddr[src]
	if routed {
		if r := c.routes[peer]; r != nil {
			r.lastRx = time.Now()
		}
	}
	c.mu.Unlock()

	if routed {
		c.deliver(inbound{payload: b, from: Addr{DeviceID: peer}})
		return
	}
	c.deliver(inbound{payload: b, from: from})
}

// readRelay drains one relay connection until it ends.
func (c *Conn) readRelay(pc *relay.PacketConn) {
	buf := make([]byte, readBuffer)
	for {
		n, from, err := pc.ReadFrom(buf)
		if err != nil {
			c.mu.Lock()
			if c.relay == pc {
				c.relay = nil
			}
			c.mu.Unlock()
			return
		}
		ra, ok := from.(relay.Addr)
		if !ok {
			continue
		}
		payload := make([]byte, n)
		copy(payload, buf[:n])

		c.mu.Lock()
		c.viaID[ra.DeviceID] = true
		c.mu.Unlock()

		c.deliver(inbound{payload: payload, from: Addr{DeviceID: ra.DeviceID}})
	}
}

// deliver queues a packet for ReadFrom, dropping it if nobody is keeping up.
func (c *Conn) deliver(p inbound) {
	select {
	case c.in <- p:
	case <-c.closed:
	default:
		// QUIC retransmits. Blocking here would stall the read loop, and with
		// it every probe and every other peer's traffic.
	}
}

// ReadFrom returns the next QUIC packet and the address to attribute it to:
// a peer's stable Addr for anything relayed or punched, the raw UDP address for
// an ordinary direct peer.
func (c *Conn) ReadFrom(p []byte) (int, net.Addr, error) {
	for {
		// The deadline is checked here, not only when the channel closes.
		// Waking on a close alone is edge-triggered, and the edge can land
		// while no reader is in the select below -- a reader arriving
		// afterwards would then wait on a channel nobody will ever close,
		// with a deadline that passed before it got here. That is not a
		// missed timeout but a permanent one: quic-go's Transport.Close
		// unblocks its read loop by setting a deadline in the past and then
		// waits for it to return, so it never returns and neither does the
		// daemon's shutdown.
		c.dmu.Lock()
		deadlineCh := c.deadlineCh
		expired := c.expiredLocked()
		c.dmu.Unlock()
		if expired {
			return 0, nil, os.ErrDeadlineExceeded
		}

		select {
		case msg := <-c.in:
			return copy(p, msg.payload), msg.from, nil
		case <-c.closed:
			return 0, nil, ErrClosed
		case <-deadlineCh:
			c.dmu.Lock()
			expired := c.expiredLocked()
			c.dmu.Unlock()
			if expired {
				return 0, nil, os.ErrDeadlineExceeded
			}
		}
	}
}

// expiredLocked reports whether a read deadline is set and already past.
// c.dmu must be held.
func (c *Conn) expiredLocked() bool {
	return !c.readDeadline.IsZero() && !time.Now().Before(c.readDeadline)
}

// WriteTo sends a packet, choosing the path.
//
// An Addr goes over the peer's direct route if one is validated and over the
// relay otherwise; a UDP address goes straight out of the socket, which is what
// an ordinary LAN session does.
func (c *Conn) WriteTo(p []byte, addr net.Addr) (int, error) {
	select {
	case <-c.closed:
		return 0, ErrClosed
	default:
	}

	target, ok := addr.(Addr)
	if !ok {
		return c.udp.WriteTo(p, addr)
	}

	c.mu.Lock()
	c.viaID[target.DeviceID] = true
	r := c.routes[target.DeviceID]
	rl := c.relay
	c.mu.Unlock()

	if r != nil {
		return c.udp.WriteTo(p, net.UDPAddrFromAddrPort(r.addr))
	}
	if rl == nil {
		return 0, fmt.Errorf("path: no route to %s and no relay connection", target.DeviceID.Fingerprint())
	}
	return rl.WriteTo(p, relay.Addr{DeviceID: target.DeviceID})
}

// LocalAddr is the shared UDP socket's address.
func (c *Conn) LocalAddr() net.Addr { return c.udp.LocalAddr() }

// Close shuts the socket. The relay connection is not closed here: it belongs
// to whoever dialled it.
func (c *Conn) Close() error {
	c.once.Do(func() {
		close(c.closed)
		_ = c.udp.Close()
	})
	return nil
}

// Done is closed when the conn is.
func (c *Conn) Done() <-chan struct{} { return c.closed }

// --- routes ------------------------------------------------------------------

// Promote makes a validated direct address the path to a peer.
//
// Everything above sees nothing: the peer's address does not change, the QUIC
// connection is not renegotiated, and a transfer in flight keeps running (PRD
// R9). Only the next packet leaves by a different route.
func (c *Conn) Promote(peer identity.DeviceID, addr netip.AddrPort, token []byte) {
	now := time.Now()

	c.mu.Lock()
	if existing := c.routes[peer]; existing != nil {
		delete(c.byAddr, existing.addr)
	}
	c.routes[peer] = &route{addr: addr, token: append([]byte(nil), token...), since: now, lastRx: now}
	c.byAddr[addr] = peer
	c.viaID[peer] = true
	c.mu.Unlock()

	c.logf("direct path to %s established at %s (%s)", peer.Fingerprint(), addr, classOf(addr))
}

// Demote abandons a peer's direct route, sending its traffic back over the
// relay.
//
// The cooldown is the hysteresis §18 asks for: a path that has just failed is
// not retried immediately, so a marginal one cannot make the session flap
// between routes instead of moving data.
func (c *Conn) Demote(peer identity.DeviceID, why string) {
	c.mu.Lock()
	r := c.routes[peer]
	if r != nil {
		delete(c.routes, peer)
		delete(c.byAddr, r.addr)
		c.cooldown[peer] = time.Now()
	}
	c.mu.Unlock()

	if r != nil {
		c.logf("direct path to %s at %s abandoned: %s", peer.Fingerprint(), r.addr, why)
	}
}

// Direct reports a peer's validated direct address, if it has one.
func (c *Conn) Direct(peer identity.DeviceID) (netip.AddrPort, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	r := c.routes[peer]
	if r == nil {
		return netip.AddrPort{}, false
	}
	return r.addr, true
}

// CanUpgrade reports whether a peer may be punched towards now, which it may
// not while its last failed path is still in cooldown.
func (c *Conn) CanUpgrade(peer identity.DeviceID) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.routes[peer] != nil {
		return false
	}
	if at, ok := c.cooldown[peer]; ok && time.Since(at) < repromoteBackoff {
		return false
	}
	return true
}

// Class is §7.2's path class for a peer: what to put in a PathInfo hint.
//
// An empty string means this Conn is not carrying that peer by DeviceID at all,
// which is an ordinary direct session dialled at an address — the caller knows
// its own class for that case and this one should not guess.
func (c *Conn) Class(peer identity.DeviceID) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if r := c.routes[peer]; r != nil {
		return classOf(r.addr)
	}
	if c.viaID[peer] {
		return ClassRelayed
	}
	return ""
}

// watchRoutes keeps direct routes honest: it pokes quiet ones so their NAT
// mapping survives, and abandons dead ones so a peer that moved network is
// reachable again over the relay rather than only in principle.
func (c *Conn) watchRoutes() {
	ticker := time.NewTicker(keepaliveInterval / 3)
	defer ticker.Stop()

	for {
		select {
		case <-c.closed:
			return
		case <-ticker.C:
			c.sweep(time.Now())
		}
	}
}

// sweep is one pass of that review, split out so the timing rules can be
// asserted without waiting for them.
//
// A route is only abandoned when there is a relay to fall back to. Without one
// there is nowhere else for the packets to go, and demoting would turn a path
// that might still recover into a session that certainly cannot send.
func (c *Conn) sweep(now time.Time) {
	type check struct {
		peer  identity.DeviceID
		addr  netip.AddrPort
		token []byte
		quiet time.Duration
	}
	var checks []check

	c.mu.Lock()
	haveRelay := c.relay != nil
	for peer, r := range c.routes {
		checks = append(checks, check{peer: peer, addr: r.addr, token: r.token, quiet: now.Sub(r.lastRx)})
	}
	c.mu.Unlock()

	for _, ch := range checks {
		switch {
		case ch.quiet > keepaliveTimeout && haveRelay:
			c.Demote(ch.peer, fmt.Sprintf("nothing received for %s", ch.quiet.Round(time.Second)))
		case ch.quiet > keepaliveInterval:
			c.sendProbe(probePing, ch.token, ch.addr)
		}
	}
}

// --- deadlines ---------------------------------------------------------------

// SetDeadline sets both deadlines. quic-go uses the read one.
func (c *Conn) SetDeadline(t time.Time) error {
	if err := c.SetReadDeadline(t); err != nil {
		return err
	}
	return c.SetWriteDeadline(t)
}

// SetReadDeadline makes a blocked ReadFrom return after t. The channel is
// replaced rather than signalled so that a waiter woken by a *changed* deadline
// re-reads it instead of reporting a timeout that has not happened.
func (c *Conn) SetReadDeadline(t time.Time) error {
	c.dmu.Lock()
	defer c.dmu.Unlock()

	if c.deadlineTimer != nil {
		c.deadlineTimer.Stop()
		c.deadlineTimer = nil
	}
	close(c.deadlineCh)
	c.deadlineCh = make(chan struct{})
	c.readDeadline = t

	if !t.IsZero() {
		ch := c.deadlineCh
		c.deadlineTimer = time.AfterFunc(time.Until(t), func() {
			c.dmu.Lock()
			defer c.dmu.Unlock()
			if c.deadlineCh == ch {
				close(c.deadlineCh)
				c.deadlineCh = make(chan struct{})
			}
		})
	}
	return nil
}

// SetWriteDeadline is passed through to the socket.
func (c *Conn) SetWriteDeadline(t time.Time) error { return c.udp.SetWriteDeadline(t) }

// timeInPast is a deadline that has already expired, used to unblock a read.
func timeInPast() time.Time { return time.Now().Add(-time.Second) }

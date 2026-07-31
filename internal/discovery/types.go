package discovery

import (
	"time"

	"github.com/shreyashsri79/openair/internal/identity"
)

// Service is the DNS-SD service type. Note the `_udp`: v2 is QUIC, so the
// transport label changes from v1.0's `_openair._tcp` (PROTOCOL.md §15.1).
// A v1.0 device and a v2 device therefore cannot see each other at all, which
// is the intended outcome -- they share no wire protocol.
const Service = "_openair._udp"

// Domain is the mDNS domain. Always "local." for link-local discovery.
const Domain = "local."

// DefaultUnicastPort is the UDP port the unicast fallback binds and beacons
// on (PROTOCOL.md §15.2). It is deliberately not 5353: the fallback exists
// for networks where multicast is filtered, and reusing the mDNS port would
// put it behind the same filter.
const DefaultUnicastPort = 53318

// ProtoVersion is the highest protocol version this build speaks, advertised
// as TXT `v` (PROTOCOL.md §15.1).
const ProtoVersion = 1

// Timing defaults. The scan window plus the pause is the worst-case time for
// an already-running device to notice a new one, and PRD R6 caps that at
// three seconds -- so their sum must stay comfortably under it.
const (
	DefaultScanWindow = 2 * time.Second
	DefaultPause      = 500 * time.Millisecond

	// DefaultTTL is how long a candidate survives without being re-heard.
	// It must exceed several scan cycles, or a peer that merely dropped one
	// multicast packet would flap out of the list and back in.
	DefaultTTL = 30 * time.Second
)

// Via records which transport produced a candidate. It is a hint for logging
// and for ordering dial attempts; it carries no authority (HLD §3.2).
type Via string

const (
	ViaMDNS    Via = "mdns"
	ViaUnicast Via = "unicast"
)

// PeerCandidate is one device this layer believes might be reachable, and the
// addresses it might be reachable at. HLD §3.2: the output of discovery is a
// stream of these into the connection manager.
//
// A candidate is a hint and nothing more. Every field in it arrived unsigned
// from the network, so anything acting on one must still authenticate the
// peer by its pinned key at the TLS handshake (PROTOCOL.md §2, §15.2). In
// particular a matching DeviceID here does NOT mean the device at Addrs holds
// the key for it.
type PeerCandidate struct {
	DeviceID identity.DeviceID

	// Addrs are dial-ready "host:port" strings, best first. There may be
	// several: a device with a wired and a wireless interface announces both,
	// and only the connection manager is in a position to race them.
	Addrs []string

	Via          Via
	DisplayName  string
	ProtoVersion uint8

	// FirstSeen and LastSeen are local clock readings, never values taken off
	// the wire.
	FirstSeen time.Time
	LastSeen  time.Time
}

// EventKind distinguishes the three things that can happen to a candidate.
type EventKind int

const (
	// PeerFound is the first sighting of a DeviceID.
	PeerFound EventKind = iota
	// PeerUpdated means an already-known device's addresses, port or display
	// name changed. A subscriber holding an open session can ignore these; one
	// drawing a device list cannot.
	PeerUpdated
	// PeerLost means nothing has been heard from the device for TTL. It does
	// not mean the device is gone -- only that discovery stopped seeing it.
	PeerLost
)

func (k EventKind) String() string {
	switch k {
	case PeerFound:
		return "found"
	case PeerUpdated:
		return "updated"
	case PeerLost:
		return "lost"
	default:
		return "unknown"
	}
}

// Event is one change to the candidate set.
//
// HLD §3.2 describes the output as a stream of PeerCandidate; the wrapper adds
// the one thing a device list cannot be built without, which is the difference
// between "appeared" and "went away".
type Event struct {
	Kind EventKind
	Peer PeerCandidate
}

// Config configures a Discovery. The zero value is not usable: DeviceID and
// Port are required, since a candidate without them is not dialable.
type Config struct {
	// DeviceID and Port are what this device announces about itself. Port is
	// the QUIC port an inbound session should be dialed on -- normally
	// conn.Listener.Addr()'s port, not the discovery port.
	DeviceID identity.DeviceID
	Port     int

	// DisplayName is TXT `n`. Free-form UTF-8, shown to a human; it is not an
	// identifier and two devices may share one.
	DisplayName string

	// BrowseOnly listens and asks, but never says anything about this device.
	//
	// It exists because a process that is not accepting sessions has no port to
	// advertise, and announcing one anyway would publish an address that
	// refuses every connection made to it. `openair send` and `openair
	// discover` browse; only a listening process announces. Port may be zero
	// when this is set.
	BrowseOnly bool

	// DisableMDNS turns off §15.1. Set by the unicast-fallback tests and by
	// anyone on a network where multicast is known-dead.
	DisableMDNS bool
	// DisableUnicast turns off §15.2.
	DisableUnicast bool

	// UnicastPort is the fallback's bind port; zero means DefaultUnicastPort.
	// Tests set it to reach two instances inside one host.
	UnicastPort int

	// UnicastPeers are extra "host:port" targets to beacon at directly --
	// §15.2's "known-last-good addresses". Announces are sent to these
	// regardless of whether broadcast works.
	UnicastPeers []string

	// DisableBroadcast suppresses the subnet-broadcast beacon, leaving only
	// UnicastPeers. Tests set it so a `go test` run does not spray the LAN.
	DisableBroadcast bool

	// ScanWindow, Pause and TTL override the timing defaults above.
	ScanWindow time.Duration
	Pause      time.Duration
	TTL        time.Duration

	// Logf receives transport-level failures. Discovery is best-effort by
	// nature -- a network that drops multicast is not an error condition -- so
	// nothing here is returned to the caller. Nil discards.
	Logf func(format string, args ...any)

	// now is injectable so the expiry sweep can be tested without sleeping.
	now func() time.Time
}

func (c *Config) scanWindow() time.Duration {
	if c.ScanWindow > 0 {
		return c.ScanWindow
	}
	return DefaultScanWindow
}

func (c *Config) pause() time.Duration {
	if c.Pause > 0 {
		return c.Pause
	}
	return DefaultPause
}

func (c *Config) ttl() time.Duration {
	if c.TTL > 0 {
		return c.TTL
	}
	return DefaultTTL
}

func (c *Config) unicastPort() int {
	if c.UnicastPort > 0 {
		return c.UnicastPort
	}
	return DefaultUnicastPort
}

func (c *Config) clock() time.Time {
	if c.now != nil {
		return c.now()
	}
	return time.Now()
}

func (c *Config) logf(format string, args ...any) {
	if c.Logf != nil {
		c.Logf(format, args...)
	}
}

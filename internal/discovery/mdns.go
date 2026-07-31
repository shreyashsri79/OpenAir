package discovery

import (
	"context"
	"net"
	"sync"
	"time"

	"github.com/grandcat/zeroconf"
)

// mdnsSource is §15.1: register `_openair._udp` and browse for it.
//
// Ported from v1.0's openair-gui/internal/sender/discover.go with three
// changes, each of which was a defect there:
//
//  1. Service type is `_openair._udp`, not `_tcp` (PROTOCOL.md §15.1).
//  2. The DNS-SD instance name is the DeviceID, not the display name. v1.0
//     used the display name, which made the instance name non-unique between
//     two machines with the same hostname and put user-controlled text into a
//     DNS label. The display name now travels in TXT `n` where §15.1 puts it.
//  3. Self-filtering is by DeviceID, not by comparing the announced IP against
//     this host's interface addresses. v1.0's test hid every peer running on
//     the same machine, which is exactly the case the tests here need, and it
//     failed to hide this device when it announced an address the local
//     interface list did not have.
type mdnsSource struct {
	cfg *Config

	mu     sync.Mutex
	server *zeroconf.Server
}

func newMDNSSource(cfg *Config) *mdnsSource {
	return &mdnsSource{cfg: cfg}
}

// announce registers this device. A registration failure is not fatal: on a
// network with no multicast-capable interface, mDNS simply does not work and
// the unicast fallback is the whole point of §15.2.
func (m *mdnsSource) announce(a Announce) error {
	server, err := zeroconf.Register(
		string(a.DeviceID), // instance name: unique by construction
		Service,
		Domain,
		a.Port,
		a.TXT(),
		nil, // all multicast-capable interfaces
	)
	if err != nil {
		return err
	}
	m.mu.Lock()
	m.server = server
	m.mu.Unlock()
	return nil
}

// run browses until ctx is cancelled, handing every well-formed foreign
// announce to sink.
//
// The loop opens a fresh Browse for each scan window rather than holding one
// open forever. That is v1.0's shape and it is not laziness: zeroconf's client
// remembers which instances it has already delivered for the life of a Browse
// and never re-delivers them, so a long-lived Browse reports each peer exactly
// once and can never refresh a liveness timestamp. Re-browsing is what keeps
// TTL expiry meaningful.
func (m *mdnsSource) run(ctx context.Context, sink func(Announce, []string, Via)) {
	for {
		m.scan(ctx, sink)
		select {
		case <-ctx.Done():
			return
		default:
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(m.cfg.pause()):
		}
	}
}

func (m *mdnsSource) scan(ctx context.Context, sink func(Announce, []string, Via)) {
	resolver, err := zeroconf.NewResolver(nil)
	if err != nil {
		m.cfg.logf("discovery: mdns resolver: %v", err)
		return
	}

	entries := make(chan *zeroconf.ServiceEntry)
	scanCtx, cancel := context.WithTimeout(ctx, m.cfg.scanWindow())
	defer cancel()

	go func() {
		if err := resolver.Browse(scanCtx, Service, Domain, entries); err != nil {
			m.cfg.logf("discovery: mdns browse: %v", err)
			cancel()
		}
	}()

	for {
		select {
		case entry, ok := <-entries:
			if !ok {
				// zeroconf closes the channel when the browse context ends.
				// Waiting for scanCtx keeps the window honest either way.
				<-scanCtx.Done()
				return
			}
			if entry == nil {
				continue
			}
			a, addrs, ok := entryToCandidate(entry)
			if !ok {
				continue
			}
			sink(a, addrs, ViaMDNS)
		case <-scanCtx.Done():
			return
		}
	}
}

// entryToCandidate converts a zeroconf entry into an announce plus dial
// addresses, or reports that it is not usable.
func entryToCandidate(entry *zeroconf.ServiceEntry) (Announce, []string, bool) {
	a, err := ParseTXT(entry.Text)
	if err != nil {
		return Announce{}, nil, false
	}

	// §15.1's TXT `port` is authoritative for the QUIC port. The SRV port
	// should agree, but the TXT record is what the spec names, and trusting
	// one field from two sources is how they drift.
	ips := make([]net.IP, 0, len(entry.AddrIPv4)+len(entry.AddrIPv6))
	ips = append(ips, entry.AddrIPv4...)
	ips = append(ips, entry.AddrIPv6...)

	addrs := rankAddrs(ips, a.Port)
	if len(addrs) == 0 {
		return Announce{}, nil, false
	}
	return a, addrs, true
}

// close withdraws the registration. zeroconf multicasts a goodbye (TTL 0) so
// peers drop this device immediately rather than waiting out their TTL.
func (m *mdnsSource) close() {
	m.mu.Lock()
	server := m.server
	m.server = nil
	m.mu.Unlock()
	if server != nil {
		server.Shutdown()
	}
}

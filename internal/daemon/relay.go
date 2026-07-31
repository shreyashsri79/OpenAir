package daemon

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/shreyashsri79/openair/internal/identity"
	"github.com/shreyashsri79/openair/internal/path"
	"github.com/shreyashsri79/openair/internal/relay"
	"github.com/shreyashsri79/openair/internal/session"
)

// Relay, M8. A device that cannot be reached directly is reached through a
// forwarder that carries ciphertext and holds no keys (§17).
//
// The daemon does two things with one relay connection: it listens on it, so
// peers can reach this device, and it dials over it when a direct path fails.
// Both are ordinary QUIC — a relayed session is indistinguishable from a direct
// one above the packet conn, which is what lets everything else stay unchanged.

// relayRetry is how long to wait before reconnecting to a relay that dropped.
// A relay connection dying is usually a network that went away, and the same
// network coming back is what fixes it.
const relayRetry = 15 * time.Second

// RelayConfig is where to connect and who to trust as the relay.
type RelayConfig struct {
	// Addr is the relay's "host:port". Empty disables relaying, which leaves a
	// device reachable exactly where a direct path exists.
	Addr string

	// ServerID is the DeviceID the relay's key must derive (§2, D-62).
	ServerID identity.DeviceID
}

// ParseRelay reads the "host:port@deviceid" form, the same as the rendezvous
// flag. Both halves are required for the same reason.
func ParseRelay(s string) (RelayConfig, error) {
	rv, err := ParseRendezvous(s)
	if err != nil {
		return RelayConfig{}, err
	}
	return RelayConfig{Addr: rv.Addr, ServerID: rv.ServerID}, nil
}

// relayState is the live relay connection and where it says this device lives.
//
// There is no listener here since M9: the relay conn is attached to the shared
// path conn, and the one QUIC listener over that conn accepts direct, relayed
// and punched peers alike. A peer arriving over the relay is the same peer.
type relayState struct {
	mu   sync.Mutex
	pc   *relay.PacketConn
	home string
}

// startRelay connects to the relay and keeps the connection up until ctx ends.
func (d *Daemon) startRelay(ctx context.Context) {
	if d.cfg.Relay.Addr == "" {
		return
	}
	go func() {
		for {
			if err := d.runRelay(ctx); err != nil && ctx.Err() == nil {
				d.cfg.Logf("relay %s: %v", d.cfg.Relay.Addr, err)
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(relayRetry):
			}
		}
	}()
}

// runRelay holds one relay connection for as long as it lasts.
func (d *Daemon) runRelay(ctx context.Context) error {
	pc, err := relay.Dial(ctx, relay.ClientConfig{
		Local:    d.id,
		Addr:     d.cfg.Relay.Addr,
		ServerID: d.cfg.Relay.ServerID,
		Logf:     d.cfg.Logf,
	})
	if err != nil {
		return err
	}
	defer pc.Close()

	// Attaching the relay to the path conn is what makes this device reachable
	// through it: the existing QUIC listener sits on that conn, so relayed
	// peers arrive on the same accept loop as direct ones and are subject to
	// the same trust store and the same capabilities. It is also what lets M9
	// move a peer off the relay later without touching the session.
	d.paths.SetRelay(pc)
	defer d.paths.SetRelay(nil)

	d.relay.mu.Lock()
	d.relay.pc = pc
	d.relay.home = fmt.Sprintf("%s@%s", d.cfg.Relay.Addr, d.cfg.Relay.ServerID)
	d.relay.mu.Unlock()

	defer func() {
		d.relay.mu.Lock()
		d.relay.pc, d.relay.home = nil, ""
		d.relay.mu.Unlock()
	}()

	d.cfg.Logf("reachable through relay %s (%s)",
		d.cfg.Relay.Addr, d.cfg.Relay.ServerID.Fingerprint())

	select {
	case <-ctx.Done():
		return nil
	case <-pc.Done():
		return fmt.Errorf("relay connection ended")
	}
}

// relayPacketConn returns the live relay conn, or nil.
func (d *Daemon) relayPacketConn() *relay.PacketConn {
	d.relay.mu.Lock()
	defer d.relay.mu.Unlock()
	return d.relay.pc
}

// relayHome is what to publish to a rendezvous server so peers know where this
// device can be reached when no direct path works (§16's relay_home field).
func (d *Daemon) relayHome() string {
	d.relay.mu.Lock()
	defer d.relay.mu.Unlock()
	return d.relay.home
}

// dialViaRelay opens a session to a paired peer through the relay.
//
// The peer is addressed by DeviceID, which is the only address a relayed path
// has, and is still authenticated by its pinned key: a relay that forwarded to
// the wrong device produces a failed handshake rather than a wrong session.
func (d *Daemon) dialViaRelay(ctx context.Context, id identity.DeviceID) (session.Session, error) {
	if d.relayPacketConn() == nil {
		return nil, fmt.Errorf("no relay connection")
	}
	peer, ok := d.store.Get(id)
	if !ok {
		return nil, fmt.Errorf("%s is not paired with this device", id.Fingerprint())
	}
	// Dialled over the path conn rather than the relay conn directly, and
	// addressed by path.Addr: the relay is where those packets go today, and
	// the address stays the same when a punch moves them onto a direct path
	// under a live session.
	return d.endpoint.Dial(ctx, path.Addr{DeviceID: id}, peer)
}

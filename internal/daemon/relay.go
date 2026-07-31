package daemon

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/shreyashsri79/openair/internal/conn"
	"github.com/shreyashsri79/openair/internal/identity"
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

// relayState is the live relay connection and the listener riding it.
type relayState struct {
	mu   sync.Mutex
	pc   *relay.PacketConn
	ln   conn.Listener
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

	// Listening on the relay is not optional: a device that only dialled over
	// it would be reachable by nobody, which is the case the relay exists for.
	ln, err := conn.ListenPacketConn(pc, d.id, d.cfg.DisplayName, platform(), d.handlers(),
		conn.ListenOptions{
			Authorize:   d.authorize,
			PeerLookup:  d.store.Get,
			OnAuthEvent: d.onAuthEvent,
		})
	if err != nil {
		return fmt.Errorf("listen over the relay: %w", err)
	}
	defer ln.Close()

	d.relay.mu.Lock()
	d.relay.pc, d.relay.ln = pc, ln
	d.relay.home = fmt.Sprintf("%s@%s", d.cfg.Relay.Addr, d.cfg.Relay.ServerID)
	d.relay.mu.Unlock()

	defer func() {
		d.relay.mu.Lock()
		d.relay.pc, d.relay.ln, d.relay.home = nil, nil, ""
		d.relay.mu.Unlock()
	}()

	d.cfg.Logf("reachable through relay %s (%s)",
		d.cfg.Relay.Addr, d.cfg.Relay.ServerID.Fingerprint())

	// Inbound relayed sessions are accepted exactly as direct ones are, so a
	// peer arriving this way is subject to the same trust store and the same
	// capabilities. A handshake failure is one peer, not the listener (D-52).
	go d.acceptFrom(ctx, ln)

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
	pc := d.relayPacketConn()
	if pc == nil {
		return nil, fmt.Errorf("no relay connection")
	}
	peer, ok := d.store.Get(id)
	if !ok {
		return nil, fmt.Errorf("%s is not paired with this device", id.Fingerprint())
	}
	return conn.DialPacketConn(ctx, pc, relay.Addr{DeviceID: id},
		d.id, d.cfg.DisplayName, platform(), d.handlers(), peer)
}

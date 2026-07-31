package daemon

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/shreyashsri79/openair/internal/identity"
	"github.com/shreyashsri79/openair/internal/rendezvous"
)

// Rendezvous, M7. The daemon publishes where this device can be reached and
// looks up peers that discovery cannot see (§16).
//
// It changes nothing about who may connect. A looked-up endpoint is still just
// an address: the peer at the other end is authenticated by its pinned key like
// any other (§2), so a rendezvous server that lies produces a failed handshake
// rather than a wrong session. That is what makes trusting an operator with
// this metadata a bounded decision.

// RendezvousConfig is where to register and who to trust as the server.
type RendezvousConfig struct {
	// Addr is the server's "host:port". Empty disables rendezvous entirely, and
	// a device with no rendezvous is exactly what M1--M6 shipped: reachable on
	// the LAN and by explicit address.
	Addr string

	// ServerID is the DeviceID the server's key must derive (§2). Required
	// whenever Addr is set -- see rendezvous.ClientConfig.ServerID for why an
	// address alone will not do.
	ServerID identity.DeviceID
}

// ParseRendezvous reads the "host:port@deviceid" form used by the flag and by
// what `openair-rendezvous` prints when it starts.
//
// One string rather than two flags because the two halves are useless apart: an
// address with no pinned ID is a server anyone can impersonate, and an ID with
// no address is nothing at all.
func ParseRendezvous(s string) (RendezvousConfig, error) {
	if s == "" {
		return RendezvousConfig{}, nil
	}
	addr, id, ok := strings.Cut(s, "@")
	if !ok {
		return RendezvousConfig{}, fmt.Errorf(
			"rendezvous %q needs the server's device id too, as host:port@deviceid "+
				"(openair-rendezvous prints the line to copy when it starts)", s)
	}
	deviceID := identity.DeviceID(strings.ReplaceAll(strings.ToLower(id), "-", ""))
	if !deviceID.Valid() {
		return RendezvousConfig{}, fmt.Errorf("rendezvous server id %q is not a device id", id)
	}
	if addr == "" {
		return RendezvousConfig{}, errors.New("rendezvous needs a host:port before the @")
	}
	return RendezvousConfig{Addr: addr, ServerID: deviceID}, nil
}

// startRendezvous begins publishing this device's endpoints, if one is
// configured. It returns immediately; registration runs until ctx ends.
func (d *Daemon) startRendezvous(ctx context.Context) {
	if d.cfg.Rendezvous.Addr == "" {
		return
	}
	client, err := rendezvous.NewClient(rendezvous.ClientConfig{
		Local:    d.id,
		Addr:     d.cfg.Rendezvous.Addr,
		ServerID: d.cfg.Rendezvous.ServerID,
		Logf:     d.cfg.Logf,
	})
	if err != nil {
		d.cfg.Logf("rendezvous disabled: %v", err)
		return
	}

	d.mu.Lock()
	d.rendezvous = client
	d.mu.Unlock()

	d.cfg.Logf("registering with rendezvous %s (%s)",
		d.cfg.Rendezvous.Addr, d.cfg.Rendezvous.ServerID.Fingerprint())

	go client.KeepRegistered(ctx, func() ([]string, string) {
		endpoints, err := rendezvous.EndpointsFor(d.ln.Addr())
		if err != nil {
			d.cfg.Logf("cannot work out what to register: %v", err)
			return nil, ""
		}
		// relay_home is empty until M8: this device is reachable at these
		// addresses or not at all, and claiming a relay it does not have would
		// send peers somewhere that cannot forward to it.
		return endpoints, ""
	})
}

func (d *Daemon) rendezvousClient() *rendezvous.Client {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.rendezvous
}

// lookupPeer asks the rendezvous server where a paired device is.
//
// Only paired devices: the answer has to be verified against a pinned key
// (§16), and a device this one has never paired with has no such key here. That
// is not a restriction the server enforces or even knows about — it is what
// makes its answers worth nothing to an attacker.
func (d *Daemon) lookupPeer(ctx context.Context, id identity.DeviceID) ([]string, error) {
	client := d.rendezvousClient()
	if client == nil {
		return nil, errors.New("no rendezvous server is configured")
	}
	peer, ok := d.store.Get(id)
	if !ok {
		return nil, fmt.Errorf("%s is not paired with this device", id.Fingerprint())
	}

	lookupCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	endpoints, _, err := client.Lookup(lookupCtx, id, peer.IdentityPublicKey)
	if err != nil {
		return nil, err
	}
	return endpoints, nil
}

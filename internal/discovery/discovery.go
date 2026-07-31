package discovery

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/shreyashsri79/openair/internal/identity"
)

// ErrNoTransport reports that every discovery transport was disabled or failed
// to start, so this Discovery can never see anything. Returned rather than
// swallowed: a device list that is permanently empty for a configuration
// reason should not look like a quiet network.
var ErrNoTransport = errors.New("discovery: no usable transport")

// Discovery runs the LAN transports and merges what they hear into one stream
// of candidate events.
//
// It never dials. Discovery's entire output is Events(); acting on a candidate
// is the connection manager's job (HLD §3.2), and keeping the two apart is
// what stops an unauthenticated broadcast from causing an outbound connection.
type Discovery struct {
	cfg  Config
	self Announce

	mdns    *mdnsSource
	unicast *unicastSource

	events chan Event

	mu    sync.Mutex
	peers map[identity.DeviceID]*PeerCandidate

	cancel context.CancelFunc
	wg     sync.WaitGroup

	closeOnce sync.Once
}

// New starts announcing this device and browsing for others. Call Close to
// stop; a Discovery that is not closed keeps beaconing.
func New(cfg Config) (*Discovery, error) {
	self := Announce{
		DeviceID:     cfg.DeviceID,
		ProtoVersion: ProtoVersion,
		Port:         cfg.Port,
		DisplayName:  cfg.DisplayName,
	}
	if cfg.BrowseOnly {
		// Nothing is published, so there is no port to check -- but the DeviceID
		// still has to be well formed, because it is what queries carry and what
		// the self-filter compares against.
		if !cfg.DeviceID.Valid() {
			return nil, fmt.Errorf("%w: device id %q", ErrMalformedAnnounce, cfg.DeviceID)
		}
	} else if err := self.Validate(); err != nil {
		return nil, fmt.Errorf("discovery: cannot announce this device: %w", err)
	}
	if cfg.DisableMDNS && cfg.DisableUnicast {
		return nil, ErrNoTransport
	}

	d := &Discovery{
		cfg:    cfg,
		self:   self,
		events: make(chan Event, 64),
		peers:  make(map[identity.DeviceID]*PeerCandidate),
	}

	ctx, cancel := context.WithCancel(context.Background())
	d.cancel = cancel

	started := 0

	if !cfg.DisableUnicast {
		u, err := newUnicastSource(&d.cfg, self)
		if err != nil {
			// A bound fallback port is the common failure here (a second
			// instance on one host). mDNS may still work, so this is logged
			// and not returned.
			d.cfg.logf("discovery: unicast fallback unavailable: %v", err)
		} else {
			d.unicast = u
			started++
			d.wg.Add(1)
			go func() {
				defer d.wg.Done()
				u.run(ctx, d.observe)
			}()
		}
	}

	if !cfg.DisableMDNS {
		m := newMDNSSource(&d.cfg)
		if !cfg.BrowseOnly {
			if err := m.announce(self); err != nil {
				d.cfg.logf("discovery: mdns registration failed: %v", err)
			}
		}
		d.mdns = m
		started++
		d.wg.Add(1)
		go func() {
			defer d.wg.Done()
			m.run(ctx, d.observe)
		}()
	}

	if started == 0 {
		cancel()
		return nil, ErrNoTransport
	}

	d.wg.Add(1)
	go func() {
		defer d.wg.Done()
		d.expireLoop(ctx)
	}()

	return d, nil
}

// Events is the candidate stream. It is buffered; a subscriber that stops
// reading loses events rather than stalling the transports, because a stalled
// beacon is worse than a stale device list.
func (d *Discovery) Events() <-chan Event { return d.events }

// Peers returns the current unexpired candidates, sorted by DeviceID so the
// order does not jitter between calls.
func (d *Discovery) Peers() []PeerCandidate {
	d.mu.Lock()
	defer d.mu.Unlock()

	out := make([]PeerCandidate, 0, len(d.peers))
	for _, p := range d.peers {
		out = append(out, *p)
	}
	slices.SortFunc(out, func(a, b PeerCandidate) int {
		switch {
		case a.DeviceID < b.DeviceID:
			return -1
		case a.DeviceID > b.DeviceID:
			return 1
		}
		return 0
	})
	return out
}

// UnicastPort reports the port the fallback bound, or zero if it is not
// running. Tests use it to point two instances at each other without
// broadcasting.
func (d *Discovery) UnicastPort() int {
	if d.unicast == nil {
		return 0
	}
	return d.unicast.localPort()
}

// observe folds one heard announce into the candidate table.
//
// This is the only place a foreign announce enters the package's state, which
// makes it the one place the self-filter has to be right.
func (d *Discovery) observe(a Announce, addrs []string, via Via) {
	if a.DeviceID == d.self.DeviceID {
		// Our own announce, heard back off the network. mDNS multicast loops
		// back to the sending host by default, so this fires constantly.
		//
		// Filtering by DeviceID rather than by comparing addresses against
		// this host's interface list (v1.0's approach) is what lets two
		// instances on one machine see each other -- the loopback case that
		// v1.0 could not represent and that every test here depends on.
		return
	}
	if err := a.Validate(); err != nil {
		return
	}
	if len(addrs) == 0 {
		return
	}

	now := d.cfg.clock()

	d.mu.Lock()
	existing, known := d.peers[a.DeviceID]
	var ev Event
	switch {
	case !known:
		p := &PeerCandidate{
			DeviceID:     a.DeviceID,
			Addrs:        addrs,
			Via:          via,
			DisplayName:  a.DisplayName,
			ProtoVersion: a.ProtoVersion,
			FirstSeen:    now,
			LastSeen:     now,
		}
		d.peers[a.DeviceID] = p
		ev = Event{Kind: PeerFound, Peer: *p}
	default:
		changed := mergeAddrs(existing, addrs) ||
			existing.DisplayName != a.DisplayName ||
			existing.ProtoVersion != a.ProtoVersion
		existing.DisplayName = a.DisplayName
		existing.ProtoVersion = a.ProtoVersion
		existing.LastSeen = now
		if via == ViaMDNS {
			// mDNS is the higher-fidelity source (it carries every interface
			// address, not just the one a datagram happened to arrive from),
			// so it wins the label when both transports see a device.
			existing.Via = via
		}
		if !changed {
			d.mu.Unlock()
			return
		}
		ev = Event{Kind: PeerUpdated, Peer: *existing}
	}
	d.mu.Unlock()

	d.emit(ev)
}

// mergeAddrs adds any address not already known, keeping the existing order
// (best-first) and appending newcomers. Reports whether anything was added.
//
// Addresses accumulate rather than replace because the two transports see
// different subsets: mDNS reports every interface a peer announced, the
// unicast fallback reports only the source address of the datagram that
// happened to arrive. Replacing on every sighting would make the list flap.
func mergeAddrs(p *PeerCandidate, addrs []string) bool {
	changed := false
	for _, a := range addrs {
		if !slices.Contains(p.Addrs, a) {
			p.Addrs = append(p.Addrs, a)
			changed = true
		}
	}
	return changed
}

// expireLoop drops candidates nothing has been heard from for TTL and emits
// PeerLost for each.
func (d *Discovery) expireLoop(ctx context.Context) {
	// Sweeping several times per TTL bounds how stale a "lost" report can be
	// without making the sweep itself a busy loop.
	interval := d.cfg.ttl() / 4
	if interval < 100*time.Millisecond {
		interval = 100 * time.Millisecond
	}
	t := time.NewTicker(interval)
	defer t.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			for _, lost := range d.sweep() {
				d.emit(Event{Kind: PeerLost, Peer: lost})
			}
		}
	}
}

func (d *Discovery) sweep() []PeerCandidate {
	cutoff := d.cfg.clock().Add(-d.cfg.ttl())

	d.mu.Lock()
	defer d.mu.Unlock()

	var lost []PeerCandidate
	for id, p := range d.peers {
		if p.LastSeen.Before(cutoff) {
			lost = append(lost, *p)
			delete(d.peers, id)
		}
	}
	return lost
}

// emit delivers an event, dropping it if the subscriber is behind. See Events.
func (d *Discovery) emit(ev Event) {
	select {
	case d.events <- ev:
	default:
		d.cfg.logf("discovery: event queue full, dropped %s for %s", ev.Kind, ev.Peer.DeviceID)
	}
}

// Close stops both transports and withdraws this device's announcement.
// Safe to call more than once.
func (d *Discovery) Close() error {
	d.closeOnce.Do(func() {
		d.cancel()
		if d.mdns != nil {
			d.mdns.close()
		}
		if d.unicast != nil {
			d.unicast.close()
		}
		d.wg.Wait()
		close(d.events)
	})
	return nil
}

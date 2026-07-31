package mobile

import (
	"sync"

	"github.com/shreyashsri79/openair/internal/discovery"
	"github.com/shreyashsri79/openair/internal/identity"
)

// Discovery finds OpenAir devices on the local network (PROTOCOL.md §15).
//
// It never dials anything. Its whole output is a list of candidates, and a
// candidate is an unauthenticated hint: a matching DeviceID does NOT mean the
// device at that address holds the key for it. Only the pinned-key handshake
// decides that, which is why Sender still refuses an unpaired peer however
// confidently discovery listed it.
type Discovery struct {
	id          *Identity
	displayName string
	port        int

	mu   sync.Mutex
	impl *discovery.Discovery
}

// NewDiscovery builds the browser.
//
// port is the port this device accepts sessions on, or 0 to browse without
// announcing. Pass 0 unless a Receiver is running: advertising a port nothing
// is listening on publishes an address that refuses every connection made to it
// (D-48). Receiver.Port() is the value to pass when one is.
func NewDiscovery(id *Identity, displayName string, port int) *Discovery {
	return &Discovery{id: id, displayName: displayName, port: port}
}

// Start begins browsing, and announcing if a port was given.
func (d *Discovery) Start() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.impl != nil {
		return ErrAlreadyRunning
	}

	impl, err := discovery.New(discovery.Config{
		DeviceID:    d.id.impl.DeviceID(),
		Port:        d.port,
		BrowseOnly:  d.port == 0,
		DisplayName: d.displayName,
	})
	if err != nil {
		return err
	}
	d.impl = impl
	return nil
}

// Stop ends browsing and withdraws this device's announcement.
func (d *Discovery) Stop() error {
	d.mu.Lock()
	impl := d.impl
	d.impl = nil
	d.mu.Unlock()
	if impl == nil {
		return nil
	}
	return impl.Close()
}

// IsRunning reports whether discovery is active.
func (d *Discovery) IsRunning() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.impl != nil
}

// Peers returns a snapshot of what is on the network right now.
//
// It is a snapshot rather than a stream because that is what a Compose list
// wants: poll it on a timer, diff it against what is displayed. gobind cannot
// carry a channel across the boundary in any case.
func (d *Discovery) Peers() *DeviceList {
	d.mu.Lock()
	impl := d.impl
	d.mu.Unlock()
	if impl == nil {
		return &DeviceList{}
	}

	candidates := impl.Peers()
	out := &DeviceList{items: make([]deviceEntry, 0, len(candidates))}
	for _, c := range candidates {
		addr := ""
		if len(c.Addrs) > 0 {
			addr = c.Addrs[0]
		}
		peer, ok := d.id.store.Get(c.DeviceID)
		out.items = append(out.items, deviceEntry{
			deviceID:    string(c.DeviceID),
			displayName: c.DisplayName,
			addr:        addr,
			via:         string(c.Via),
			paired:      ok && peer.Level > identity.LevelUnpaired,
		})
	}
	return out
}

// deviceEntry is one row of a DeviceList.
type deviceEntry struct {
	deviceID    string
	displayName string
	addr        string
	via         string
	paired      bool
}

// DeviceList is a snapshot of discovered devices.
//
// gobind carries no slices of structs, so a list is an object with an index
// accessor. Every getter returns a zero value for an out-of-range index rather
// than panicking: a UI thread racing a refresh must not crash the app.
type DeviceList struct {
	items []deviceEntry
}

// Len reports how many devices are in the snapshot.
func (l *DeviceList) Len() int { return len(l.items) }

// DeviceID returns the device identifier at i.
func (l *DeviceList) DeviceID(i int) string {
	if e, ok := l.at(i); ok {
		return e.deviceID
	}
	return ""
}

// Fingerprint returns the device identifier at i, grouped for display.
func (l *DeviceList) Fingerprint(i int) string {
	return FormatFingerprint(l.DeviceID(i))
}

// DisplayName returns the name the device calls itself, which may be empty and
// is not unique: it is a label, never an identifier.
func (l *DeviceList) DisplayName(i int) string {
	if e, ok := l.at(i); ok {
		return e.displayName
	}
	return ""
}

// Addr returns the best address to dial the device at i, ready to pass to
// Sender.SendFiles.
func (l *DeviceList) Addr(i int) string {
	if e, ok := l.at(i); ok {
		return e.addr
	}
	return ""
}

// Via reports which transport found the device: "mdns" or "unicast". A hint for
// display and logging; it carries no authority.
func (l *DeviceList) Via(i int) string {
	if e, ok := l.at(i); ok {
		return e.via
	}
	return ""
}

// Paired reports whether this device holds pinned keys for the device at i.
//
// It is answered from the local trust store, not from anything the peer said.
// "Paired" here means "we hold a key for this DeviceID", not "this device has
// proved anything" -- the proof happens at the handshake.
func (l *DeviceList) Paired(i int) bool {
	if e, ok := l.at(i); ok {
		return e.paired
	}
	return false
}

func (l *DeviceList) at(i int) (deviceEntry, bool) {
	if i < 0 || i >= len(l.items) {
		return deviceEntry{}, false
	}
	return l.items[i], true
}

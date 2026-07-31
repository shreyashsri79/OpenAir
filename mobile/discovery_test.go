package mobile

import (
	"errors"
	"testing"

	"github.com/shreyashsri79/openair/internal/identity"
)

// The binding's own surface: lifecycle, and accessors that a UI thread racing a
// refresh must not be able to crash the app with.
//
// The transport behaviour underneath -- two instances finding each other, the
// unicast fallback, a hostile announce -- is tested in internal/discovery,
// where the ports can be controlled so a `go test` run does not announce this
// machine to whatever network it is on.
func TestDiscoveryLifecycle(t *testing.T) {
	id := newIdentity(t)

	// Port 0: browse without announcing, which is what a shell does when no
	// Receiver is running (D-48).
	d := NewDiscovery(id, "test-phone", 0)
	if d.IsRunning() {
		t.Fatal("a fresh Discovery reports itself running")
	}
	if l := d.Peers(); l == nil || l.Len() != 0 {
		t.Fatal("a stopped Discovery returned peers")
	}

	if err := d.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer d.Stop()

	if !d.IsRunning() {
		t.Fatal("Start did not mark the Discovery running")
	}
	if err := d.Start(); !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("second Start returned %v, want ErrAlreadyRunning", err)
	}

	if err := d.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if d.IsRunning() {
		t.Fatal("Stop left the Discovery running")
	}
	// Stopping twice is what happens when a shell tears down on both a lifecycle
	// callback and a user action.
	if err := d.Stop(); err != nil {
		t.Fatalf("second Stop: %v", err)
	}
}

// gobind has no slices of structs, so a list is an object with an index
// accessor -- which makes out-of-range access a real path rather than a
// theoretical one. Every getter answers a zero value rather than panicking.
func TestDeviceListOutOfRangeIsSafe(t *testing.T) {
	l := &DeviceList{items: []deviceEntry{{
		deviceID:    "abcdefghijklmnop",
		displayName: "desktop-home",
		addr:        "192.168.1.5:9000",
		via:         "mdns",
		paired:      true,
	}}}

	if l.Len() != 1 {
		t.Fatalf("Len = %d, want 1", l.Len())
	}
	if got := l.DeviceID(0); got != "abcdefghijklmnop" {
		t.Fatalf("DeviceID(0) = %q", got)
	}
	if got, want := l.Fingerprint(0), "abcd-efgh-ijkl-mnop"; got != want {
		t.Fatalf("Fingerprint(0) = %q, want %q", got, want)
	}
	if got := l.DisplayName(0); got != "desktop-home" {
		t.Fatalf("DisplayName(0) = %q", got)
	}
	if got := l.Addr(0); got != "192.168.1.5:9000" {
		t.Fatalf("Addr(0) = %q", got)
	}
	if got := l.Via(0); got != "mdns" {
		t.Fatalf("Via(0) = %q", got)
	}
	if !l.Paired(0) {
		t.Fatal("Paired(0) = false")
	}

	for _, i := range []int{-1, 1, 99} {
		if got := l.DeviceID(i); got != "" {
			t.Errorf("DeviceID(%d) = %q, want empty", i, got)
		}
		if got := l.DisplayName(i); got != "" {
			t.Errorf("DisplayName(%d) = %q, want empty", i, got)
		}
		if got := l.Addr(i); got != "" {
			t.Errorf("Addr(%d) = %q, want empty", i, got)
		}
		if got := l.Via(i); got != "" {
			t.Errorf("Via(%d) = %q, want empty", i, got)
		}
		if l.Paired(i) {
			t.Errorf("Paired(%d) = true, want false", i)
		}
	}
}

// Paired is answered from the local trust store, never from what the peer said
// about itself -- a device on the network claiming a DeviceID has proved
// nothing until the pinned-key handshake.
func TestDiscoveryPairedComesFromTheTrustStore(t *testing.T) {
	a, b := newIdentity(t), newIdentity(t)

	d := NewDiscovery(a, "phone", 0)
	list := &DeviceList{}
	for _, id := range []string{a.DeviceID(), b.DeviceID()} {
		peer, ok := d.id.store.Get(identity.DeviceID(id))
		list.items = append(list.items, deviceEntry{
			deviceID: id,
			paired:   ok && peer.Level > 0,
		})
	}
	if list.Paired(0) || list.Paired(1) {
		t.Fatal("a device was reported paired before any pairing happened")
	}

	pairIdentities(t, a, b)
	if !a.IsPaired(b.DeviceID()) {
		t.Fatal("IsPaired disagrees with the store it reads")
	}
}

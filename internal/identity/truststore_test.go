package identity

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// testPeer builds a well-formed trust store record. The DeviceID is derived
// from the identity key rather than invented, because Put insists the two agree.
func testPeer(t *testing.T, name string) Peer {
	t.Helper()
	idPub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	privPub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return Peer{
		DeviceID:            DeriveDeviceID(idPub),
		IdentityPublicKey:   idPub,
		PrivilegePublicKey:  privPub,
		DisplayName:         name,
		Platform:            "linux",
		Level:               LevelTrusted,
		GrantedCapabilities: []byte{1, 2},
		AuthPolicy:          "timed",
		TokenGrantedAt:      1700000000,
		ProtectionTier:      TierKeystore,
		CreatedAt:           1699999999,
		LastSeen:            1700000001,
	}
}

func newTestStore(t *testing.T) (*FileTrustStore, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "trust.json")
	s, err := OpenTrustStore(path)
	if err != nil {
		t.Fatalf("OpenTrustStore: %v", err)
	}
	return s, path
}

func TestTrustStoreStartsEmpty(t *testing.T) {
	s, _ := newTestStore(t)
	if got := s.List(); len(got) != 0 {
		t.Fatalf("fresh store holds %d peers", len(got))
	}
	if _, ok := s.Get("kzdvvj2umnduyauf"); ok {
		t.Fatal("fresh store returned a peer")
	}
}

func TestTrustStoreRoundTrip(t *testing.T) {
	s, _ := newTestStore(t)
	want := testPeer(t, "desktop-home")

	if err := s.Put(want); err != nil {
		t.Fatalf("Put: %v", err)
	}
	got, ok := s.Get(want.DeviceID)
	if !ok {
		t.Fatal("Get missed a peer that was just Put")
	}
	assertPeerEqual(t, got, want)

	list := s.List()
	if len(list) != 1 {
		t.Fatalf("List returned %d peers, want 1", len(list))
	}
	assertPeerEqual(t, list[0], want)
}

// TestTrustStoreSurvivesRestart is the property that matters: D-7 pinning means
// a store that forgets its peers has unpaired every device on the network.
func TestTrustStoreSurvivesRestart(t *testing.T) {
	s, path := newTestStore(t)
	a := testPeer(t, "desktop-home")
	b := testPeer(t, "phone")
	b.Level = LevelOwned
	b.AuthPolicy = "never"
	b.ProtectionTier = TierKeystore

	if err := s.Put(a); err != nil {
		t.Fatal(err)
	}
	if err := s.Put(b); err != nil {
		t.Fatal(err)
	}

	reopened, err := OpenTrustStore(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if got := len(reopened.List()); got != 2 {
		t.Fatalf("reopened store holds %d peers, want 2", got)
	}
	for _, want := range []Peer{a, b} {
		got, ok := reopened.Get(want.DeviceID)
		if !ok {
			t.Fatalf("peer %s did not survive restart", want.DeviceID)
		}
		assertPeerEqual(t, got, want)
	}
}

func TestTrustStorePutReplaces(t *testing.T) {
	s, path := newTestStore(t)
	p := testPeer(t, "laptop")
	if err := s.Put(p); err != nil {
		t.Fatal(err)
	}

	p.DisplayName = "laptop-renamed"
	p.LastSeen = 1800000000
	p.GrantedCapabilities = []byte{1, 2, 3}
	if err := s.Put(p); err != nil {
		t.Fatal(err)
	}
	if got := len(s.List()); got != 1 {
		t.Fatalf("replacing a peer produced %d records", got)
	}
	reopened, err := OpenTrustStore(path)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := reopened.Get(p.DeviceID)
	assertPeerEqual(t, got, p)
}

func TestTrustStoreDelete(t *testing.T) {
	s, path := newTestStore(t)
	p := testPeer(t, "phone")
	if err := s.Put(p); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(p.DeviceID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, ok := s.Get(p.DeviceID); ok {
		t.Fatal("Get returned a deleted peer")
	}
	if err := s.Delete(p.DeviceID); !errors.Is(err, ErrUnknownPeer) {
		t.Errorf("second Delete = %v, want ErrUnknownPeer", err)
	}
	reopened, err := OpenTrustStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(reopened.List()); got != 0 {
		t.Fatalf("deletion did not persist: %d peers remain", got)
	}
}

// TestTrustStoreReturnsCopies guards the one thing a mutex cannot: Peer carries
// three byte slices, and handing out the store's own arrays would let a caller
// rewrite a pinned key from outside the lock.
func TestTrustStoreReturnsCopies(t *testing.T) {
	s, _ := newTestStore(t)
	p := testPeer(t, "phone")
	original := append(ed25519.PublicKey(nil), p.IdentityPublicKey...)
	if err := s.Put(p); err != nil {
		t.Fatal(err)
	}

	// Mutating the caller's copy after Put must not reach the store.
	p.IdentityPublicKey[0] ^= 0xff
	p.GrantedCapabilities[0] = 99

	got, _ := s.Get(DeriveDeviceID(original))
	if !got.IdentityPublicKey.Equal(original) {
		t.Fatal("mutating the argument to Put changed the stored key")
	}

	// Mutating a returned record must not reach the store either.
	got.IdentityPublicKey[0] ^= 0xff
	got.GrantedCapabilities[0] = 77
	again, _ := s.Get(DeriveDeviceID(original))
	if !again.IdentityPublicKey.Equal(original) {
		t.Fatal("mutating a Get result changed the stored key")
	}
	if again.GrantedCapabilities[0] == 77 {
		t.Fatal("mutating a Get result changed the stored capabilities")
	}
}

func TestTrustStoreRejectsMalformedPeers(t *testing.T) {
	s, _ := newTestStore(t)
	good := testPeer(t, "ok")

	cases := []struct {
		name string
		f    func(p *Peer)
	}{
		{"empty device id", func(p *Peer) { p.DeviceID = "" }},
		{"uppercase device id", func(p *Peer) { p.DeviceID = "KZDVVJ2UMNDUYAUF" }},
		{"device id not derived from the key", func(p *Peer) { p.DeviceID = "kzdvvj2umnduyauf" }},
		{"short identity key", func(p *Peer) { p.IdentityPublicKey = []byte{1, 2, 3} }},
		{"short privilege key", func(p *Peer) { p.PrivilegePublicKey = []byte{1, 2, 3} }},
		{"owned at tier none", func(p *Peer) { p.Level = LevelOwned; p.ProtectionTier = TierNone }},
		{"owned with no privilege key", func(p *Peer) { p.Level = LevelOwned; p.PrivilegePublicKey = nil }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := clonePeer(good)
			c.f(&p)
			if err := s.Put(p); err == nil {
				t.Fatal("Put accepted a malformed peer")
			}
		})
	}
	if got := len(s.List()); got != 0 {
		t.Fatalf("rejected peers still landed in the store: %d records", got)
	}
}

// A device at protection tier none holds no privilege key (D-21 tier 3) and
// must never be recorded as Owned; Trusted is fine.
func TestTrustStoreAcceptsTierNoneAtTrusted(t *testing.T) {
	s, _ := newTestStore(t)
	p := testPeer(t, "old-laptop")
	p.Level = LevelTrusted
	p.ProtectionTier = TierNone
	p.PrivilegePublicKey = nil
	if err := s.Put(p); err != nil {
		t.Fatalf("Put: %v", err)
	}
	got, ok := s.Get(p.DeviceID)
	if !ok {
		t.Fatal("peer not stored")
	}
	if got.PrivilegePublicKey != nil {
		t.Error("a tier-none peer came back with a privilege key")
	}
}

func TestTrustStoreRejectsCorruptFile(t *testing.T) {
	dir := t.TempDir()

	bad := filepath.Join(dir, "garbage.json")
	if err := os.WriteFile(bad, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenTrustStore(bad); err == nil {
		t.Error("OpenTrustStore accepted invalid JSON")
	}

	wrongVersion := filepath.Join(dir, "v99.json")
	if err := os.WriteFile(wrongVersion, []byte(`{"version":99,"peers":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenTrustStore(wrongVersion); err == nil {
		t.Error("OpenTrustStore accepted an unknown schema version")
	}

	// A record whose DeviceID does not derive from its own key is not a
	// recoverable state: something rewrote the file.
	tampered := filepath.Join(dir, "tampered.json")
	s, err := OpenTrustStore(tampered)
	if err != nil {
		t.Fatal(err)
	}
	p := testPeer(t, "phone")
	if err := s.Put(p); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(tampered)
	if err != nil {
		t.Fatal(err)
	}
	other := testPeer(t, "other")
	swapped := []byte(replaceOnce(string(raw), string(p.DeviceID), string(other.DeviceID)))
	if err := os.WriteFile(tampered, swapped, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenTrustStore(tampered); err == nil {
		t.Error("OpenTrustStore accepted a record whose DeviceID does not match its key")
	}
}

func replaceOnce(s, old, new string) string {
	for i := 0; i+len(old) <= len(s); i++ {
		if s[i:i+len(old)] == old {
			return s[:i] + new + s[i+len(old):]
		}
	}
	return s
}

func TestTrustStoreEmptyPathRejected(t *testing.T) {
	if _, err := OpenTrustStore(""); err == nil {
		t.Fatal("OpenTrustStore accepted an empty path")
	}
}

// TestTrustStoreConcurrent hammers the store from several goroutines. The
// daemon reads it on every inbound connection while pairing and last-seen
// updates write to it, so "safe for concurrent use" is a hard requirement, not
// an aspiration. Run under -race.
func TestTrustStoreConcurrent(t *testing.T) {
	s, path := newTestStore(t)

	const (
		writers = 4
		readers = 8
		rounds  = 40
	)

	peers := make([]Peer, writers)
	for i := range peers {
		peers[i] = testPeer(t, fmt.Sprintf("device-%d", i))
		if err := s.Put(peers[i]); err != nil {
			t.Fatal(err)
		}
	}

	var wg sync.WaitGroup
	errCh := make(chan error, writers*rounds)

	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			p := clonePeer(peers[w])
			for r := 0; r < rounds; r++ {
				p.LastSeen = int64(r)
				p.DisplayName = fmt.Sprintf("device-%d-%d", w, r)
				if err := s.Put(p); err != nil {
					errCh <- err
					return
				}
			}
		}(w)
	}

	// Churn: a peer that is added and removed repeatedly while readers walk the
	// store, so List and Get see a moving target.
	churn := testPeer(t, "churn")
	wg.Add(1)
	go func() {
		defer wg.Done()
		for r := 0; r < rounds; r++ {
			if err := s.Put(churn); err != nil {
				errCh <- err
				return
			}
			if err := s.Delete(churn.DeviceID); err != nil && !errors.Is(err, ErrUnknownPeer) {
				errCh <- err
				return
			}
		}
	}()

	for rd := 0; rd < readers; rd++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for r := 0; r < rounds; r++ {
				for _, p := range s.List() {
					if !p.DeviceID.Valid() {
						errCh <- fmt.Errorf("List returned a malformed DeviceID %q", p.DeviceID)
						return
					}
					if DeriveDeviceID(p.IdentityPublicKey) != p.DeviceID {
						errCh <- fmt.Errorf("List returned a torn record for %s", p.DeviceID)
						return
					}
					// Write through the returned copy: if it aliased the
					// store's own memory, the race detector would fire.
					p.IdentityPublicKey[0] ^= 0xff
				}
				for i := range peers {
					if got, ok := s.Get(peers[i].DeviceID); ok {
						if !got.IdentityPublicKey.Equal(peers[i].IdentityPublicKey) {
							errCh <- fmt.Errorf("Get returned a mutated key for %s", peers[i].DeviceID)
							return
						}
					}
				}
			}
		}()
	}

	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Error(err)
	}

	// The file must still parse after all that: every Put rewrote it.
	reopened, err := OpenTrustStore(path)
	if err != nil {
		t.Fatalf("reopen after concurrent writes: %v", err)
	}
	if got := len(reopened.List()); got != writers {
		t.Fatalf("reopened store holds %d peers, want %d", got, writers)
	}
}

func assertPeerEqual(t *testing.T, got, want Peer) {
	t.Helper()
	if got.DeviceID != want.DeviceID {
		t.Errorf("DeviceID = %q, want %q", got.DeviceID, want.DeviceID)
	}
	if !got.IdentityPublicKey.Equal(want.IdentityPublicKey) {
		t.Error("IdentityPublicKey differs")
	}
	if len(want.PrivilegePublicKey) > 0 && !got.PrivilegePublicKey.Equal(want.PrivilegePublicKey) {
		t.Error("PrivilegePublicKey differs")
	}
	if got.DisplayName != want.DisplayName {
		t.Errorf("DisplayName = %q, want %q", got.DisplayName, want.DisplayName)
	}
	if got.Platform != want.Platform {
		t.Errorf("Platform = %q, want %q", got.Platform, want.Platform)
	}
	if got.Level != want.Level {
		t.Errorf("Level = %v, want %v", got.Level, want.Level)
	}
	if string(got.GrantedCapabilities) != string(want.GrantedCapabilities) {
		t.Errorf("GrantedCapabilities = %v, want %v", got.GrantedCapabilities, want.GrantedCapabilities)
	}
	if got.AuthPolicy != want.AuthPolicy {
		t.Errorf("AuthPolicy = %q, want %q", got.AuthPolicy, want.AuthPolicy)
	}
	if got.TokenGrantedAt != want.TokenGrantedAt {
		t.Errorf("TokenGrantedAt = %d, want %d", got.TokenGrantedAt, want.TokenGrantedAt)
	}
	if got.ProtectionTier != want.ProtectionTier {
		t.Errorf("ProtectionTier = %v, want %v", got.ProtectionTier, want.ProtectionTier)
	}
	if got.CreatedAt != want.CreatedAt {
		t.Errorf("CreatedAt = %d, want %d", got.CreatedAt, want.CreatedAt)
	}
	if got.LastSeen != want.LastSeen {
		t.Errorf("LastSeen = %d, want %d", got.LastSeen, want.LastSeen)
	}
}

package main

import (
	"bytes"
	"path/filepath"
	"sync"
	"testing"

	"github.com/shreyashsri79/openair/internal/identity"
)

// pairKeyDirs pins each of two key directories in the other's trust store, as
// if `openair pair` had already been run between them.
//
// It writes the same records the pairing exchange writes, rather than driving
// the exchange itself: the exchange has its own end-to-end test in
// internal/pairing, and what the CLI tests need is the state it leaves behind.
func pairKeyDirs(t *testing.T, dirA, dirB string) (idA, idB identity.DeviceID) {
	t.Helper()

	a, err := loadIdentity(dirA)
	if err != nil {
		t.Fatalf("identity in %s: %v", dirA, err)
	}
	b, err := loadIdentity(dirB)
	if err != nil {
		t.Fatalf("identity in %s: %v", dirB, err)
	}

	pin := func(dir string, peer identity.Identity) {
		t.Helper()
		store, err := identity.OpenTrustStore(filepath.Join(dir, trustStoreName))
		if err != nil {
			t.Fatalf("trust store in %s: %v", dir, err)
		}
		err = store.Put(identity.Peer{
			DeviceID:          peer.DeviceID(),
			IdentityPublicKey: peer.IdentityPublic(),
			DisplayName:       "test peer",
			Platform:          "linux",
			Level:             identity.LevelTrusted,
			AuthPolicy:        "timed",
			CreatedAt:         1,
			LastSeen:          1,
		})
		if err != nil {
			t.Fatalf("pin peer in %s: %v", dir, err)
		}
	}
	pin(dirA, b)
	pin(dirB, a)
	return a.DeviceID(), b.DeviceID()
}

// lockedBuffer is a bytes.Buffer safe for the receiver's goroutines to write
// to while the test goroutine reads it in a failure message. Without it the
// race detector fires on the diagnostic path rather than on anything real,
// which is a good way to lose an afternoon.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

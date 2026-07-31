package identity

import (
	"crypto/ed25519"
	"errors"
	"sync"
	"testing"
	"time"
)

// clock is a hand-wound clock. The unlock session's whole subject is a six-hour
// boundary, and a test that waited for one would not be run.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func newClock() *clock { return &clock{t: time.Date(2026, 7, 31, 9, 0, 0, 0, time.UTC)} }

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// unlockable builds a TierPassphrase identity with a hand-wound clock. Argon2id
// is dialled down to its minimum: the parameters are what the format stores and
// the test is not measuring them, so paying a second per unlock would only make
// the suite slow.
func unlockable(t *testing.T) (*FileIdentity, *clock) {
	t.Helper()
	c := newClock()
	id, err := LoadOrCreate(Options{
		Dir:        t.TempDir(),
		Tier:       TierPassphrase,
		Passphrase: []byte(testPassphrase),
		Argon2:     testArgon2,
		Now:        c.now,
	})
	if err != nil {
		t.Fatalf("LoadOrCreate: %v", err)
	}
	return id, c
}

func (i *FileIdentity) holdsKey() bool {
	i.unlock.mu.Lock()
	defer i.unlock.mu.Unlock()
	return i.unlock.key != nil
}

func peerID(t *testing.T) DeviceID {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	return DeriveDeviceID(pub)
}

// TestUnlockThenSignOwned is the happy path, verified the way a peer verifies
// it: rebuild §6's signing input independently and check the signature against
// the privilege public key.
func TestUnlockThenSignOwned(t *testing.T) {
	id, _ := unlockable(t)
	target := peerID(t)

	expiry, err := id.Unlock(target, UnlockOptions{Passphrase: []byte(testPassphrase)})
	if err != nil {
		t.Fatalf("Unlock: %v", err)
	}
	if want := id.now().Add(DefaultTokenLifetime); !expiry.Equal(want) {
		t.Errorf("expiry %s, want %s (D-18's six hours, absolute from grant)", expiry, want)
	}

	nonce, issuedAt, sig, err := id.SignOwned(target, 0x03, 7)
	if err != nil {
		t.Fatalf("SignOwned: %v", err)
	}
	if len(nonce) != OwnedNonceLen {
		t.Errorf("nonce is %d bytes, want %d", len(nonce), OwnedNonceLen)
	}
	input, err := OwnedSigningInput(target, 0x03, 7, nonce, issuedAt)
	if err != nil {
		t.Fatal(err)
	}
	if !ed25519.Verify(id.PrivilegePublic(), input, sig) {
		t.Fatal("the signature does not verify against this device's privilege key")
	}
}

// TestSignOwnedIsScopedPerPeer is D-30 as a property rather than a comment: one
// unlock authorises one device, and reaching a second needs its own unlock.
func TestSignOwnedIsScopedPerPeer(t *testing.T) {
	id, _ := unlockable(t)
	a, b := peerID(t), peerID(t)

	if _, err := id.Unlock(a, UnlockOptions{Passphrase: []byte(testPassphrase)}); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := id.SignOwned(a, 1, 1); err != nil {
		t.Fatalf("signing for the unlocked peer: %v", err)
	}
	if _, _, _, err := id.SignOwned(b, 1, 1); !errors.Is(err, ErrLocked) {
		t.Fatalf("signing for a peer that was never unlocked: %v, want ErrLocked", err)
	}
	if _, err := id.BeginOwned(b); !errors.Is(err, ErrLocked) {
		t.Fatalf("BeginOwned for a peer that was never unlocked: %v, want ErrLocked", err)
	}
}

// TestUnlockExpiryWipesTheKey is D-19's central claim: expiry is a wipe, not a
// policy check, so there is nothing left to sign with afterwards.
func TestUnlockExpiryWipesTheKey(t *testing.T) {
	id, c := unlockable(t)
	target := peerID(t)

	if _, err := id.Unlock(target, UnlockOptions{Passphrase: []byte(testPassphrase), Lifetime: time.Hour}); err != nil {
		t.Fatal(err)
	}
	if !id.holdsKey() {
		t.Fatal("no decrypted key is held while a session is live")
	}

	c.advance(time.Hour + time.Second)
	if _, _, _, err := id.SignOwned(target, 1, 1); !errors.Is(err, ErrLocked) {
		t.Fatalf("SignOwned after expiry: %v, want ErrLocked", err)
	}
	if id.holdsKey() {
		t.Fatal("the decrypted privilege key is still in memory after the token expired")
	}
	if _, ok := id.UnlockedUntil(target); ok {
		t.Error("UnlockedUntil still reports a live session after expiry")
	}
}

// TestExpiryRejectsNewWorkAndLetsRunningWorkFinish is PROTOCOL.md §6.5, which
// is two rules that only make sense together: the timer must not destroy a
// transfer that was authorised when it started, and it must not be extendable
// indefinitely by starting one just before the boundary.
func TestExpiryRejectsNewWorkAndLetsRunningWorkFinish(t *testing.T) {
	id, c := unlockable(t)
	target := peerID(t)

	if _, err := id.Unlock(target, UnlockOptions{
		Passphrase: []byte(testPassphrase),
		Lifetime:   time.Hour,
	}); err != nil {
		t.Fatal(err)
	}

	op, err := id.BeginOwned(target)
	if err != nil {
		t.Fatalf("BeginOwned while unlocked: %v", err)
	}

	// Past expiry: the running operation continues, new ones do not start.
	c.advance(time.Hour + time.Minute)
	if err := op.Check(); err != nil {
		t.Fatalf("an operation begun before expiry was stopped by it: %v", err)
	}
	if _, err := id.BeginOwned(target); !errors.Is(err, ErrLocked) {
		t.Fatalf("a new operation started after expiry: %v, want ErrLocked", err)
	}
	if _, _, _, err := id.SignOwned(target, 1, 1); !errors.Is(err, ErrLocked) {
		t.Fatalf("a new proof was signed after expiry: %v, want ErrLocked", err)
	}

	// Still inside the one-hour grace.
	c.advance(50 * time.Minute)
	if err := op.Check(); err != nil {
		t.Fatalf("operation refused inside the one-hour grace: %v", err)
	}

	// Past it.
	c.advance(11 * time.Minute)
	if err := op.Check(); !errors.Is(err, ErrGraceExpired) {
		t.Fatalf("operation past expiry + grace: %v, want ErrGraceExpired", err)
	}
}

// TestPolicyNeverDoesNotExpire is D-20's always-on designation. It is the same
// toggle as D-18's opt-in "never", not a second one.
func TestPolicyNeverDoesNotExpire(t *testing.T) {
	id, c := unlockable(t)
	target := peerID(t)

	expiry, err := id.Unlock(target, UnlockOptions{Passphrase: []byte(testPassphrase), Policy: PolicyNever})
	if err != nil {
		t.Fatal(err)
	}
	if !expiry.IsZero() {
		t.Errorf("PolicyNever reported expiry %s, want the zero time", expiry)
	}

	op, err := id.BeginOwned(target)
	if err != nil {
		t.Fatal(err)
	}
	c.advance(30 * 24 * time.Hour)
	if _, _, _, err := id.SignOwned(target, 1, 1); err != nil {
		t.Fatalf("SignOwned a month later under PolicyNever: %v", err)
	}
	if err := op.Check(); err != nil {
		t.Fatalf("Check a month later under PolicyNever: %v", err)
	}
}

// TestLockPeerLeavesOtherSessions checks the wipe rule: the key is held because
// grants justify it, and it goes when the last one does -- not before.
func TestLockPeerLeavesOtherSessions(t *testing.T) {
	id, _ := unlockable(t)
	a, b := peerID(t), peerID(t)

	for _, p := range []DeviceID{a, b} {
		if _, err := id.Unlock(p, UnlockOptions{Passphrase: []byte(testPassphrase)}); err != nil {
			t.Fatal(err)
		}
	}
	id.LockPeer(a)

	if _, _, _, err := id.SignOwned(a, 1, 1); !errors.Is(err, ErrLocked) {
		t.Fatalf("signing for a locked peer: %v, want ErrLocked", err)
	}
	if _, _, _, err := id.SignOwned(b, 1, 1); err != nil {
		t.Fatalf("locking one peer ended another peer's session: %v", err)
	}
	if !id.holdsKey() {
		t.Fatal("the key was wiped while a session was still live")
	}

	id.Lock()
	if id.holdsKey() {
		t.Fatal("Lock left the decrypted key in memory")
	}
	if peers := id.UnlockedPeers(); len(peers) != 0 {
		t.Errorf("UnlockedPeers after Lock: %v, want none", peers)
	}
}

// TestUnlockRejectsWrongPassphrase: a wrong passphrase must not leave a key
// behind, and must not be distinguishable from a tampered file (the AEAD cannot
// tell them apart, deliberately).
func TestUnlockRejectsWrongPassphrase(t *testing.T) {
	id, _ := unlockable(t)
	target := peerID(t)

	if _, err := id.Unlock(target, UnlockOptions{Passphrase: []byte("wrong")}); !errors.Is(err, ErrPassphrase) {
		t.Fatalf("Unlock with a wrong passphrase: %v, want ErrPassphrase", err)
	}
	if id.holdsKey() {
		t.Fatal("a failed unlock left a decrypted key in memory")
	}
	if _, _, _, err := id.SignOwned(target, 1, 1); !errors.Is(err, ErrLocked) {
		t.Fatalf("SignOwned after a failed unlock: %v, want ErrLocked", err)
	}
}

// TestUnlockThrottlesRepeatedFailures covers D-19's rate limit on the
// interactive path. It bounds guessing at a keyboard and nothing else, which is
// why the tiers in D-21 exist.
func TestUnlockThrottlesRepeatedFailures(t *testing.T) {
	id, c := unlockable(t)
	target := peerID(t)

	for n := 0; n < throttleAfter; n++ {
		if _, err := id.Unlock(target, UnlockOptions{Passphrase: []byte("wrong")}); !errors.Is(err, ErrPassphrase) {
			t.Fatalf("attempt %d: %v, want ErrPassphrase", n+1, err)
		}
	}
	if _, err := id.Unlock(target, UnlockOptions{Passphrase: []byte(testPassphrase)}); !errors.Is(err, ErrThrottled) {
		t.Fatalf("the correct passphrase was accepted while throttled: %v, want ErrThrottled", err)
	}

	c.advance(throttleCeiling + time.Second)
	if _, err := id.Unlock(target, UnlockOptions{Passphrase: []byte(testPassphrase)}); err != nil {
		t.Fatalf("unlock after the throttle elapsed: %v", err)
	}
}

// TestTierNoneCannotUnlock is D-21 tier 3: a device that cannot protect a
// privilege key holds none, so Owned is unreachable rather than weakly
// available.
func TestTierNoneCannotUnlock(t *testing.T) {
	id, err := LoadOrCreate(Options{Dir: t.TempDir(), Tier: TierNone})
	if err != nil {
		t.Fatal(err)
	}
	target := peerID(t)

	if _, err := id.Unlock(target, UnlockOptions{Passphrase: []byte(testPassphrase)}); !errors.Is(err, ErrNoPrivilegeKey) {
		t.Fatalf("Unlock at tier none: %v, want ErrNoPrivilegeKey", err)
	}
	if _, _, _, err := id.SignOwned(target, 1, 1); !errors.Is(err, ErrNoPrivilegeKey) {
		t.Fatalf("SignOwned at tier none: %v, want ErrNoPrivilegeKey", err)
	}
	if _, err := id.BeginOwned(target); !errors.Is(err, ErrNoPrivilegeKey) {
		t.Fatalf("BeginOwned at tier none: %v, want ErrNoPrivilegeKey", err)
	}
	if id.PrivilegePublic() != nil {
		t.Error("a tier 3 device reported a privilege public key")
	}
}

// TestExpiryWarningFires covers §6.5's 15-minute notice. The offset is
// configurable so the test does not have to be 15 minutes long; the default is
// the spec's.
func TestExpiryWarningFires(t *testing.T) {
	warned := make(chan DeviceID, 1)
	id, err := LoadOrCreate(Options{
		Dir:             t.TempDir(),
		Tier:            TierPassphrase,
		Passphrase:      []byte(testPassphrase),
		Argon2:          testArgon2,
		WarnBefore:      150 * time.Millisecond,
		OnExpiryWarning: func(target DeviceID, _ time.Time) { warned <- target },
	})
	if err != nil {
		t.Fatal(err)
	}
	target := peerID(t)
	if _, err := id.Unlock(target, UnlockOptions{
		Passphrase: []byte(testPassphrase),
		Lifetime:   250 * time.Millisecond,
	}); err != nil {
		t.Fatal(err)
	}

	select {
	case got := <-warned:
		if got != target {
			t.Errorf("warning named %s, want %s", got, target)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no expiry warning arrived before the token lapsed")
	}
}

// TestUnlockRelocksOnRealTime checks that the key leaves memory on its own,
// without a caller asking. Expiry that only takes effect when somebody looks
// would leave the key resident for as long as the daemon is idle.
func TestUnlockRelocksOnRealTime(t *testing.T) {
	id, err := LoadOrCreate(Options{
		Dir:        t.TempDir(),
		Tier:       TierPassphrase,
		Passphrase: []byte(testPassphrase),
		Argon2:     testArgon2,
		WarnBefore: -1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := id.Unlock(peerID(t), UnlockOptions{
		Passphrase: []byte(testPassphrase),
		Lifetime:   100 * time.Millisecond,
	}); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for id.holdsKey() {
		if time.Now().After(deadline) {
			t.Fatal("the key was still in memory long after the token expired, with nobody asking")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestUnlockedKeyIsHeldOnce guards the design note in unlockState: two peers
// unlocked at once share one decrypted key rather than holding a copy each.
func TestUnlockedKeyIsHeldOnce(t *testing.T) {
	id, _ := unlockable(t)
	a, b := peerID(t), peerID(t)
	for _, p := range []DeviceID{a, b} {
		if _, err := id.Unlock(p, UnlockOptions{Passphrase: []byte(testPassphrase)}); err != nil {
			t.Fatal(err)
		}
	}

	id.unlock.mu.Lock()
	key := id.unlock.key
	id.unlock.mu.Unlock()
	if key == nil {
		t.Fatal("no key held with two live sessions")
	}
	if !key.locked {
		// Not a failure: RLIMIT_MEMLOCK is small in some containers, and the
		// design degrades rather than refusing. Swappable is how a user is told.
		t.Logf("privilege key pages are not locked into RAM on this machine; Swappable reports %v", id.Swappable())
	}
	if got := len(id.UnlockedPeers()); got != 2 {
		t.Errorf("UnlockedPeers reports %d peers, want 2", got)
	}
}

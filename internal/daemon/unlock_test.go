package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shreyashsri79/openair/internal/identity"
	openairv1 "github.com/shreyashsri79/openair/internal/wire/openair/v1"
)

const testPassphrase = "four words make a passphrase"

// cheapArgon keeps the suite fast. The parameters live in the sealed file, so
// what is being tested -- unlock, expiry, refusal -- is unaffected by them; a
// second per unlock would only make this slower to run.
var cheapArgon = identity.Argon2Params{Time: 1, Memory: 8, Lanes: 1}

// protect creates the privilege key a daemon would otherwise start without,
// the way `openair protect` does before the daemon reads the directory.
func protect(t *testing.T, keyDir string) {
	t.Helper()
	if _, err := identity.LoadOrCreate(identity.Options{
		Dir:        keyDir,
		Tier:       identity.TierPassphrase,
		Passphrase: []byte(testPassphrase),
		Argon2:     cheapArgon,
	}); err != nil {
		t.Fatalf("protect %s: %v", keyDir, err)
	}
}

func newProtectedDaemon(t *testing.T) *Daemon {
	t.Helper()
	return newTestDaemon(t, func(cfg *Config) { protect(t, cfg.KeyDir) })
}

// pinOwned writes what pairing plus a local promotion would have left behind:
// both keys pinned, and each device recorded as Owned by the other.
func pinOwned(t *testing.T, a, b *Daemon) {
	t.Helper()
	pin := func(store identity.TrustStore, peer *Daemon) {
		err := store.Put(identity.Peer{
			DeviceID:           peer.id.DeviceID(),
			IdentityPublicKey:  peer.id.IdentityPublic(),
			PrivilegePublicKey: peer.id.PrivilegePublic(),
			DisplayName:        peer.cfg.DisplayName,
			Platform:           "linux",
			Level:              identity.LevelOwned,
			AuthPolicy:         identity.PolicyTimed,
			ProtectionTier:     identity.TierPassphrase,
			CreatedAt:          1,
			LastSeen:           1,
		})
		if err != nil {
			t.Fatalf("pin peer: %v", err)
		}
	}
	pin(a.store, b)
	pin(b.store, a)
}

func writeFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// fileDigest is digest() over a path, since these tests compare what was sent
// against what landed rather than two buffers already in hand.
func fileDigest(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return digest(b)
}

// TestUnlockedOwnedTransferNeedsNoWatcher is what M6 is for.
//
// Nobody is subscribed on the receiving daemon, so an ordinary transfer would be
// refused -- that is M4's rule and it is deliberate. An Owned peer that has
// unlocked within the last six hours gets through anyway, because the offer
// carries a proof that a human authenticated on the sending machine (§6, PRD R3).
func TestUnlockedOwnedTransferNeedsNoWatcher(t *testing.T) {
	sender := newProtectedDaemon(t)
	receiver := newProtectedDaemon(t)
	pinOwned(t, sender, receiver)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// The unlock names the peer it authorises: scope is per device (D-30).
	sc := connect(t, sender, nil, nil)
	if _, err := sc.Unlock(ctx, string(receiver.DeviceID()), []byte(testPassphrase), nil, false, 0); err != nil {
		t.Fatalf("Unlock: %v", err)
	}

	src := writeFile(t, t.TempDir(), "unattended.txt", strings.Repeat("owned bytes\n", 400))
	resp, err := sc.Send(ctx, receiver.Addr(), []string{src})
	if err != nil {
		t.Fatalf("Send with nobody watching the receiver: %v", err)
	}
	if resp.GetTransferId() == "" {
		t.Fatal("no transfer id came back")
	}

	got := filepath.Join(receiver.cfg.DestDir, "unattended.txt")
	if fileDigest(t, got) != fileDigest(t, src) {
		t.Fatal("the file that arrived is not the file that was sent")
	}
}

// TestLockedSenderStillNeedsAWatcher is the same setup with the one difference
// that matters: no unlock. The transfer is refused, because "Owned" describes
// what a device may do once someone has authenticated, not a standing licence.
func TestLockedSenderStillNeedsAWatcher(t *testing.T) {
	sender := newProtectedDaemon(t)
	receiver := newProtectedDaemon(t)
	pinOwned(t, sender, receiver)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	sc := connect(t, sender, nil, nil)
	src := writeFile(t, t.TempDir(), "no-unlock.txt", "should not arrive")

	if _, err := sc.Send(ctx, receiver.Addr(), []string{src}); err == nil {
		t.Fatal("a locked sender reached a receiver nobody was watching")
	}
	if _, err := os.Stat(filepath.Join(receiver.cfg.DestDir, "no-unlock.txt")); err == nil {
		t.Fatal("the file was written despite the refusal")
	}
}

// TestUnlockExpiryStopsNewTransfers covers §6.5 through the daemon: once the
// token lapses, the next offer arrives unproven and is refused again.
func TestUnlockExpiryStopsNewTransfers(t *testing.T) {
	sender := newProtectedDaemon(t)
	receiver := newProtectedDaemon(t)
	pinOwned(t, sender, receiver)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	sc := connect(t, sender, nil, nil)
	if _, err := sc.Unlock(ctx, string(receiver.DeviceID()), []byte(testPassphrase), nil, false, 400*time.Millisecond); err != nil {
		t.Fatalf("Unlock: %v", err)
	}

	src := writeFile(t, t.TempDir(), "first.txt", "while unlocked")
	if _, err := sc.Send(ctx, receiver.Addr(), []string{src}); err != nil {
		t.Fatalf("send while unlocked: %v", err)
	}

	// Let the token lapse. The key is wiped, so there is nothing left to sign
	// the next offer with.
	time.Sleep(700 * time.Millisecond)

	second := writeFile(t, t.TempDir(), "second.txt", "after expiry")
	if _, err := sc.Send(ctx, receiver.Addr(), []string{second}); err == nil {
		t.Fatal("a transfer went through unattended after the unlock expired")
	}
}

// TestUnlockRefusesWrongPassphrase: the daemon's answer to a bad credential is
// a refusal, and it must not leave a session behind.
func TestUnlockRefusesWrongPassphrase(t *testing.T) {
	d := newProtectedDaemon(t)
	peer := newProtectedDaemon(t)
	pinOwned(t, d, peer)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	c := connect(t, d, nil, nil)
	if _, err := c.Unlock(ctx, string(peer.DeviceID()), []byte("not the passphrase"), nil, false, 0); err == nil {
		t.Fatal("a wrong passphrase unlocked the privilege key")
	}
	if _, ok := d.id.UnlockedUntil(peer.DeviceID()); ok {
		t.Fatal("a failed unlock left a live session")
	}

	st, err := c.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(st.GetUnlockedDevices()) != 0 {
		t.Fatalf("status reports %v unlocked after a failed attempt", st.GetUnlockedDevices())
	}
}

// TestStatusAndDevicesReportUnlockState: a per-peer scope nobody can see is a
// scope nobody can trust. D-30's argument is that the prompt names the device,
// and that only holds if the state is inspectable afterwards.
func TestStatusAndDevicesReportUnlockState(t *testing.T) {
	d := newProtectedDaemon(t)
	peer := newProtectedDaemon(t)
	pinOwned(t, d, peer)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	c := connect(t, d, nil, nil)
	if _, err := c.Unlock(ctx, string(peer.DeviceID()), []byte(testPassphrase), nil, false, 0); err != nil {
		t.Fatal(err)
	}

	st, err := c.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if st.GetProtectionTier() != openairv1.ProtectionTier_PROTECTION_TIER_PASSPHRASE {
		t.Errorf("status reports tier %v, want passphrase", st.GetProtectionTier())
	}
	if got := st.GetUnlockedDevices(); len(got) != 1 || got[0] != string(peer.DeviceID()) {
		t.Fatalf("status reports %v unlocked, want just %s", got, peer.DeviceID())
	}

	devices, err := c.Devices(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	var found *openairv1.DaemonDevice
	for _, dev := range devices {
		if dev.GetDeviceId() == string(peer.DeviceID()) {
			found = dev
		}
	}
	if found == nil {
		t.Fatal("the paired device is missing from the device list")
	}
	if found.GetUnlockedUntilUnixMs() <= 0 {
		t.Errorf("device list reports unlocked_until %d, want a future time", found.GetUnlockedUntilUnixMs())
	}
	if !found.GetPrivilegeKeyPinned() {
		t.Error("device list says no privilege key is pinned for a peer that has one")
	}
}

// TestDemotionEndsTheUnlockSession: taking Owned away has to take the live
// session with it, or the demotion would be advisory until the timer ran out.
func TestDemotionEndsTheUnlockSession(t *testing.T) {
	d := newProtectedDaemon(t)
	peer := newProtectedDaemon(t)
	pinOwned(t, d, peer)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	c := connect(t, d, nil, nil)
	if _, err := c.Unlock(ctx, string(peer.DeviceID()), []byte(testPassphrase), nil, false, 0); err != nil {
		t.Fatal(err)
	}

	level, err := c.Trust(ctx, string(peer.DeviceID()), openairv1.TrustLevel_TRUST_LEVEL_TRUSTED, "")
	if err != nil {
		t.Fatalf("Trust: %v", err)
	}
	if level != openairv1.TrustLevel_TRUST_LEVEL_TRUSTED {
		t.Fatalf("Trust reported %v, want trusted", level)
	}
	if _, ok := d.id.UnlockedUntil(peer.DeviceID()); ok {
		t.Fatal("demotion left the unlock session running")
	}
	if stored, _ := d.store.Get(peer.DeviceID()); stored.Level != identity.LevelTrusted {
		t.Fatalf("trust store still records level %v", stored.Level)
	}
}

// TestPromotionRefusesAPeerWithNoPrivilegeKey is D-21 tier 3 at the point a
// user would meet it: promoting a device that protects nothing must fail with a
// reason, not succeed into an access that cannot work.
func TestPromotionRefusesAPeerWithNoPrivilegeKey(t *testing.T) {
	d := newProtectedDaemon(t)
	peer := newTestDaemon(t, nil) // no `protect`, so tier 3
	pinEachOther(t, d, peer)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	c := connect(t, d, nil, nil)
	_, err := c.Trust(ctx, string(peer.DeviceID()), openairv1.TrustLevel_TRUST_LEVEL_OWNED, "")
	if err == nil {
		t.Fatal("a device with no privilege key was promoted to owned")
	}
	if !strings.Contains(err.Error(), "privilege key") {
		t.Errorf("the refusal does not say why: %v", err)
	}
	if stored, _ := d.store.Get(peer.DeviceID()); stored.Level == identity.LevelOwned {
		t.Fatal("the trust store recorded the promotion anyway")
	}
}

// TestUnlockRefusedAtTierThree: a device that never ran `protect` holds no
// privilege key, so there is nothing to unlock and the error says so.
func TestUnlockRefusedAtTierThree(t *testing.T) {
	d := newTestDaemon(t, nil)
	peer := newProtectedDaemon(t)
	pinEachOther(t, d, peer)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	c := connect(t, d, nil, nil)
	_, err := c.Unlock(ctx, string(peer.DeviceID()), []byte(testPassphrase), nil, false, 0)
	if err == nil {
		t.Fatal("a device with no privilege key produced an unlock session")
	}
	if !strings.Contains(err.Error(), "privilege key") {
		t.Errorf("the refusal does not say why: %v", err)
	}
}

// TestAuthEventsReachTheSessionLog is PRD R4's local log, checked where it
// lands: a refusal a user cannot find afterwards is not a log.
func TestAuthEventsReachTheSessionLog(t *testing.T) {
	d := newProtectedDaemon(t)
	peer := newProtectedDaemon(t)
	pinOwned(t, d, peer)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	c := connect(t, d, nil, nil)
	if _, err := c.Unlock(ctx, string(peer.DeviceID()), []byte(testPassphrase), nil, false, 0); err != nil {
		t.Fatal(err)
	}

	b, err := os.ReadFile(filepath.Join(d.keyDir, authLogFile))
	if err != nil {
		t.Fatalf("no session log was written: %v", err)
	}
	if !strings.Contains(string(b), "unlocked") || !strings.Contains(string(b), string(peer.DeviceID())) {
		t.Fatalf("the session log does not record the unlock:\n%s", b)
	}
}

// TestUnlockUnknownPeerIsRefused: unlock is per peer, so it needs a peer. An
// unlock for a device that was never paired would be a token with no scope.
func TestUnlockUnknownPeerIsRefused(t *testing.T) {
	d := newProtectedDaemon(t)
	stranger := newProtectedDaemon(t)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	c := connect(t, d, nil, nil)
	_, err := c.Unlock(ctx, string(stranger.DeviceID()), []byte(testPassphrase), nil, false, 0)
	if err == nil {
		t.Fatal("an unpaired device was unlocked")
	}
	if errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("the request timed out rather than being refused")
	}
}

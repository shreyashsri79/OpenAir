package mobile

import (
	"crypto/rand"
	"strings"
	"testing"
	"time"

	"github.com/shreyashsri79/openair/internal/identity"
)

// testKEK stands in for what the Android Keystore releases after a biometric
// or device-credential challenge: 32 bytes this side never generates for
// itself, which is the property that makes tier 1 tier 1.
func testKEK(t *testing.T) []byte {
	t.Helper()
	kek := make([]byte, keystoreKEKLen)
	if _, err := rand.Read(kek); err != nil {
		t.Fatal(err)
	}
	return kek
}

func protectedIdentity(t *testing.T) (*Identity, []byte) {
	t.Helper()
	id, err := LoadIdentity(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if id.ProtectionTier() != TierNone {
		t.Fatalf("a fresh identity reports tier %d, want none", id.ProtectionTier())
	}
	kek := testKEK(t)
	if err := id.Protect(kek); err != nil {
		t.Fatalf("Protect: %v", err)
	}
	if id.ProtectionTier() != TierKeystore {
		t.Fatalf("after Protect the tier is %d, want keystore", id.ProtectionTier())
	}
	return id, kek
}

// pinOwnedIdentities is pairIdentities plus the privilege keys and the
// promotion, which is what a paired-then-promoted pair looks like on disk.
func pinOwnedIdentities(t *testing.T, a, b *Identity) {
	t.Helper()
	pin := func(holder, peer *Identity) {
		err := holder.store.Put(identity.Peer{
			DeviceID:           peer.impl.DeviceID(),
			IdentityPublicKey:  peer.impl.IdentityPublic(),
			PrivilegePublicKey: peer.impl.PrivilegePublic(),
			DisplayName:        "test peer",
			Platform:           PlatformName,
			Level:              identity.LevelOwned,
			AuthPolicy:         identity.PolicyTimed,
			ProtectionTier:     identity.TierKeystore,
			CreatedAt:          1,
			LastSeen:           1,
		})
		if err != nil {
			t.Fatalf("pin peer: %v", err)
		}
	}
	pin(a, b)
	pin(b, a)
}

// TestProtectAndUnlockThroughBinding is M6 as an Android shell drives it: the
// keystore hands over a key-encryption key, the core unlocks one peer, and the
// state is readable afterwards.
func TestProtectAndUnlockThroughBinding(t *testing.T) {
	id, kek := protectedIdentity(t)
	peer, _ := protectedIdentity(t)
	pinOwnedIdentities(t, id, peer)

	target := string(peer.impl.DeviceID())
	expiry, err := id.Unlock(target, kek, "", false, 0)
	if err != nil {
		t.Fatalf("Unlock: %v", err)
	}
	if expiry <= time.Now().UnixMilli() {
		t.Fatalf("expiry %d is not in the future", expiry)
	}
	if got := id.UnlockedUntil(target); got != expiry {
		t.Errorf("UnlockedUntil reports %d, want %d", got, expiry)
	}

	id.LockPeer(target)
	if got := id.UnlockedUntil(target); got != 0 {
		t.Errorf("UnlockedUntil reports %d after LockPeer, want 0", got)
	}
}

// TestUnlockRefusesTheWrongKeystoreKey: a key that did not seal the container
// does not open it, and the failure leaves no session behind.
func TestUnlockRefusesTheWrongKeystoreKey(t *testing.T) {
	id, _ := protectedIdentity(t)
	peer, _ := protectedIdentity(t)
	pinOwnedIdentities(t, id, peer)

	target := string(peer.impl.DeviceID())
	if _, err := id.Unlock(target, testKEK(t), "", false, 0); err == nil {
		t.Fatal("the wrong key-encryption key opened the privilege key")
	}
	if got := id.UnlockedUntil(target); got != 0 {
		t.Fatalf("a failed unlock left a session until %d", got)
	}
}

// TestUnlockNeverExpirePolicy is D-20's always-on designation, which a shell
// has to be able to distinguish from "locked" -- both have no expiry timestamp.
func TestUnlockNeverExpirePolicy(t *testing.T) {
	id, kek := protectedIdentity(t)
	peer, _ := protectedIdentity(t)
	pinOwnedIdentities(t, id, peer)

	target := string(peer.impl.DeviceID())
	expiry, err := id.Unlock(target, kek, "", true, 0)
	if err != nil {
		t.Fatal(err)
	}
	if expiry != 0 {
		t.Errorf("never-expire reported expiry %d, want 0", expiry)
	}
	if got := id.UnlockedUntil(target); got != -1 {
		t.Errorf("UnlockedUntil reports %d, want -1 for an always-on session", got)
	}
}

// TestUnlockRefusesUnpairedPeer: the scope of an unlock is one paired device
// (D-30), so there is nothing to unlock for a stranger.
func TestUnlockRefusesUnpairedPeer(t *testing.T) {
	id, kek := protectedIdentity(t)
	stranger, err := LoadIdentity(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	if _, err := id.Unlock(string(stranger.impl.DeviceID()), kek, "", false, 0); err == nil {
		t.Fatal("an unpaired device was unlocked")
	}
}

// TestTierThreeCannotUnlock is D-21 tier 3 through the binding: a device that
// never ran Protect holds no privilege key, and the error says so rather than
// failing obscurely at the first Owned request.
func TestTierThreeCannotUnlock(t *testing.T) {
	id, err := LoadIdentity(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	peer, _ := protectedIdentity(t)
	pairIdentities(t, id, peer)

	if id.HasPrivilegeKey() {
		t.Fatal("an unprotected device reports a privilege key")
	}
	_, err = id.Unlock(string(peer.impl.DeviceID()), testKEK(t), "", false, 0)
	if err == nil {
		t.Fatal("a device with no privilege key produced an unlock session")
	}
	if !strings.Contains(err.Error(), "privilege key") {
		t.Errorf("the refusal does not say why: %v", err)
	}
}

// TestSetOwnedRefusesAPeerWithNoPrivilegeKey covers the pairing that predates
// Protect: the record has no privilege key to verify against, so promoting it
// would create an access that can never work.
func TestSetOwnedRefusesAPeerWithNoPrivilegeKey(t *testing.T) {
	id, _ := protectedIdentity(t)
	peer, err := LoadIdentity(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	pairIdentities(t, id, peer)

	target := string(peer.impl.DeviceID())
	if err := id.SetOwned(target, true); err == nil {
		t.Fatal("a peer with no pinned privilege key was promoted")
	}
	if id.TrustLevel(target) != int(identity.LevelTrusted) {
		t.Fatalf("trust level is %d after a refused promotion", id.TrustLevel(target))
	}
}

// TestSetOwnedThenWithdrawEndsTheSession: taking Owned away must take the live
// unlock with it, or the withdrawal would be advisory until the timer ran out.
func TestSetOwnedThenWithdrawEndsTheSession(t *testing.T) {
	id, kek := protectedIdentity(t)
	peer, _ := protectedIdentity(t)
	pinOwnedIdentities(t, id, peer)

	target := string(peer.impl.DeviceID())
	if _, err := id.Unlock(target, kek, "", false, 0); err != nil {
		t.Fatal(err)
	}
	if err := id.SetOwned(target, false); err != nil {
		t.Fatalf("SetOwned(false): %v", err)
	}
	if got := id.UnlockedUntil(target); got != 0 {
		t.Fatalf("withdrawing owned access left a session until %d", got)
	}
	if id.TrustLevel(target) != int(identity.LevelTrusted) {
		t.Errorf("trust level is %d, want trusted", id.TrustLevel(target))
	}
}

// TestProtectIsOnceOnly: replacing the privilege key would invalidate every
// pairing that pinned it, so a second call is refused rather than obeyed.
func TestProtectIsOnceOnly(t *testing.T) {
	id, _ := protectedIdentity(t)
	if err := id.Protect(testKEK(t)); err == nil {
		t.Fatal("Protect replaced an existing privilege key")
	}
	if err := id.ProtectWithPassphrase("four words make a passphrase"); err == nil {
		t.Fatal("ProtectWithPassphrase replaced an existing privilege key")
	}
}

// TestTierSurvivesReload is why LoadIdentity detects rather than configures: an
// app restart must not silently drop a protected device back to tier 3.
func TestTierSurvivesReload(t *testing.T) {
	dir := t.TempDir()
	first, err := LoadIdentity(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Protect(testKEK(t)); err != nil {
		t.Fatal(err)
	}

	again, err := LoadIdentity(dir)
	if err != nil {
		t.Fatalf("reopening a protected identity: %v", err)
	}
	if again.ProtectionTier() != TierKeystore {
		t.Fatalf("after reload the tier is %d, want keystore", again.ProtectionTier())
	}
	if string(again.impl.DeviceID()) != string(first.impl.DeviceID()) {
		t.Fatal("the DeviceID changed across a reload")
	}
}

// TestUnattendedOfferNeedsNoVerifier is the receiving half on Android: with no
// offer verifier installed, an ordinary peer is refused and an unlocked Owned
// peer is not. That is the difference between "the phone is in a pocket" and
// "the phone is unreachable".
func TestUnattendedOfferNeedsNoVerifier(t *testing.T) {
	receiver, _ := protectedIdentity(t)
	sender, senderKEK := protectedIdentity(t)
	pinOwnedIdentities(t, receiver, sender)

	recv := NewReceiver(receiver, "phone", t.TempDir())
	if err := recv.Start("127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	defer recv.Stop()
	// No SetOfferVerifier: nobody is looking at this device.

	dir := t.TempDir()
	path, _ := writeFile(t, dir, "unattended.bin", 4096)
	list := NewFileList()
	if err := list.Add(path, "unattended.bin"); err != nil {
		t.Fatal(err)
	}

	send := NewSender(sender, "laptop")
	if _, err := send.SendFiles(recv.Addr(), list); err == nil {
		t.Fatal("a locked sender reached a device with nobody watching")
	}

	if _, err := sender.Unlock(string(receiver.impl.DeviceID()), senderKEK, "", false, 0); err != nil {
		t.Fatalf("Unlock: %v", err)
	}
	if _, err := send.SendFiles(recv.Addr(), list); err != nil {
		t.Fatalf("an unlocked owned sender was refused: %v", err)
	}
}

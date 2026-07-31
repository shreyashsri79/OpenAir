package identity

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

const testPassphrase = "correct horse battery staple"

func TestLoadOrCreateGeneratesAndReloads(t *testing.T) {
	dir := t.TempDir()
	opts := Options{
		Dir:        dir,
		Tier:       TierPassphrase,
		Passphrase: []byte(testPassphrase),
		Argon2:     testArgon2,
	}

	first, err := LoadOrCreate(opts)
	if err != nil {
		t.Fatalf("LoadOrCreate: %v", err)
	}
	if !first.DeviceID().Valid() {
		t.Fatalf("DeviceID %q is malformed", first.DeviceID())
	}
	if first.DeviceID() != DeriveDeviceID(first.IdentityPublic()) {
		t.Error("DeviceID does not derive from the identity key")
	}

	for _, name := range []string{identityKeyFile, privilegeKeyFile, privilegePubFile} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("expected %s on disk: %v", name, err)
		}
	}

	// Keys must survive a reload: every peer has pinned the identity key, so a
	// regenerated one is a silent unpair of every device.
	second, err := LoadOrCreate(opts)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if second.DeviceID() != first.DeviceID() {
		t.Errorf("DeviceID changed across reload: %q then %q", first.DeviceID(), second.DeviceID())
	}
	if !second.IdentityPublic().Equal(first.IdentityPublic()) {
		t.Error("identity public key changed across reload")
	}
	if !second.PrivilegePublic().Equal(first.PrivilegePublic()) {
		t.Error("privilege public key changed across reload")
	}

	// The reloaded key must still terminate TLS -- a key that parsed but signs
	// differently would only show up at handshake time.
	a, err := LoadOrCreate(opts)
	if err != nil {
		t.Fatal(err)
	}
	b := newTestIdentity(t)
	clientCfg, err := a.TLSConfig(b.IdentityPublic())
	if err != nil {
		t.Fatal(err)
	}
	serverCfg, err := b.TLSConfig(a.IdentityPublic())
	if err != nil {
		t.Fatal(err)
	}
	res := handshake(t, clientCfg, serverCfg)
	if res.clientErr != nil || res.serverErr != nil {
		t.Fatalf("reloaded identity failed to handshake: client %v, server %v", res.clientErr, res.serverErr)
	}
}

func TestLoadOrCreateDistinctDevices(t *testing.T) {
	a := newTestIdentity(t)
	b := newTestIdentity(t)
	if a.DeviceID() == b.DeviceID() {
		t.Fatal("two fresh installs produced the same DeviceID")
	}
	if a.IdentityPublic().Equal(a.PrivilegePublic()) {
		t.Fatal("the identity key and the privilege key are the same key (D-20 requires two)")
	}
}

func TestKeyFilePermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX file modes are not meaningful on Windows")
	}
	dir := t.TempDir()
	if _, err := LoadOrCreate(Options{
		Dir:        dir,
		Tier:       TierPassphrase,
		Passphrase: []byte(testPassphrase),
		Argon2:     testArgon2,
	}); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{identityKeyFile, privilegeKeyFile, privilegePubFile} {
		fi, err := os.Stat(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if got := fi.Mode().Perm(); got != 0o600 {
			t.Errorf("%s mode = %o, want 600", name, got)
		}
	}
	fi, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := fi.Mode().Perm(); got&0o077 != 0 {
		t.Errorf("key directory mode = %o, which is group- or world-accessible", got)
	}
}

// TestIdentityKeyIsNotSealed records a deliberate asymmetry rather than an
// oversight: D-20 keeps the identity key warm so the device stays reachable
// with nobody present, which means it sits on disk unencrypted at 0600 and the
// threat model has to say so.
func TestIdentityKeyIsNotSealed(t *testing.T) {
	dir := t.TempDir()
	id, err := LoadOrCreate(Options{Dir: dir, Tier: TierNone})
	if err != nil {
		t.Fatal(err)
	}
	der, err := os.ReadFile(filepath.Join(dir, identityKeyFile))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.HasPrefix(der, keyFileMagic[:]) {
		t.Fatal("the identity key is in an at-rest container; D-20 requires it usable with no unlock")
	}
	reloaded, err := LoadOrCreate(Options{Dir: dir, Tier: TierNone})
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.DeviceID() != id.DeviceID() {
		t.Error("identity key did not survive reload at tier none")
	}
}

func TestTierNoneHasNoPrivilegeKey(t *testing.T) {
	dir := t.TempDir()
	id, err := LoadOrCreate(Options{Dir: dir, Tier: TierNone})
	if err != nil {
		t.Fatalf("LoadOrCreate: %v", err)
	}
	if id.PrivilegePublic() != nil {
		t.Error("tier none produced a privilege key (D-21 tier 3 holds none)")
	}
	if id.ProtectionTier() != TierNone {
		t.Errorf("ProtectionTier = %v, want TierNone", id.ProtectionTier())
	}
	if _, err := os.Stat(filepath.Join(dir, privilegeKeyFile)); !errors.Is(err, os.ErrNotExist) {
		t.Error("tier none wrote a privilege key file")
	}
	// Tier 3 cannot reach Owned at all, which is a different failure from
	// "locked": no unlock will ever help.
	if _, _, _, err := id.SignOwned("kzdvvj2umnduyauf", 1, 2); !errors.Is(err, ErrNoPrivilegeKey) {
		t.Errorf("SignOwned at tier none = %v, want ErrNoPrivilegeKey", err)
	}
}

func TestSignOwnedIsLocked(t *testing.T) {
	id := newTestIdentity(t)
	nonce, issuedAt, sig, err := id.SignOwned("kzdvvj2umnduyauf", 1, 2)
	if !errors.Is(err, ErrLocked) {
		t.Fatalf("SignOwned = %v, want ErrLocked (M1 has no unlock session)", err)
	}
	if nonce != nil || sig != nil || issuedAt != 0 {
		t.Error("SignOwned returned material alongside ErrLocked")
	}
}

func TestPrivilegeKeyStaysSealedAtRest(t *testing.T) {
	dir := t.TempDir()
	id, err := LoadOrCreate(Options{
		Dir:        dir,
		Tier:       TierPassphrase,
		Passphrase: []byte(testPassphrase),
		Argon2:     testArgon2,
	})
	if err != nil {
		t.Fatal(err)
	}

	blob, err := os.ReadFile(filepath.Join(dir, privilegeKeyFile))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(blob, keyFileMagic[:]) {
		t.Fatal("privilege key is not in an Appendix A container")
	}

	// The unlock session is M6. What M1 must prove is that the container it
	// wrote can be opened later with the right passphrase and not otherwise.
	priv, err := id.openPrivilege([]byte(testPassphrase), nil)
	if err != nil {
		t.Fatalf("openPrivilege: %v", err)
	}
	if !matchesPublic(priv, id.PrivilegePublic()) {
		t.Fatal("unsealed privilege key does not match the advertised public key")
	}
	if _, err := id.openPrivilege([]byte("not the passphrase"), nil); !errors.Is(err, ErrPassphrase) {
		t.Errorf("openPrivilege with a wrong passphrase = %v, want ErrPassphrase", err)
	}

	// A signature made with the unsealed key must verify against the public key
	// a peer would have pinned at pairing (§5.2, §6).
	msg, err := OwnedSigningInput(id.DeviceID(), 3, 7, make([]byte, OwnedNonceLen), 1234567890)
	if err != nil {
		t.Fatal(err)
	}
	if !ed25519.Verify(id.PrivilegePublic(), msg, ed25519.Sign(priv, msg)) {
		t.Fatal("privilege key does not verify against its advertised public key")
	}
}

// TestPrivilegePubIsCrossChecked covers the workaround forced by Appendix A
// having no field for the public key: privilege.pub is a separate,
// unauthenticated file, so swapping it must be caught rather than believed.
func TestPrivilegePubIsCrossChecked(t *testing.T) {
	dir := t.TempDir()
	opts := Options{Dir: dir, Tier: TierPassphrase, Passphrase: []byte(testPassphrase), Argon2: testArgon2}
	if _, err := LoadOrCreate(opts); err != nil {
		t.Fatal(err)
	}

	other, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, privilegePubFile), other, 0o600); err != nil {
		t.Fatal(err)
	}
	id, err := LoadOrCreate(opts)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := id.openPrivilege([]byte(testPassphrase), nil); !errors.Is(err, ErrKeyFileFormat) {
		t.Fatalf("openPrivilege with a substituted public key = %v, want ErrKeyFileFormat", err)
	}
}

func TestTierMismatchOnReload(t *testing.T) {
	dir := t.TempDir()
	if _, err := LoadOrCreate(Options{
		Dir: dir, Tier: TierPassphrase, Passphrase: []byte(testPassphrase), Argon2: testArgon2,
	}); err != nil {
		t.Fatal(err)
	}

	// Reopening at tier none would silently drop the device out of Owned.
	if _, err := LoadOrCreate(Options{Dir: dir, Tier: TierNone}); !errors.Is(err, ErrTierMismatch) {
		t.Errorf("reload at tier none = %v, want ErrTierMismatch", err)
	}
	// Reopening at tier keystore would look for a KDF the file does not use.
	_, err := LoadOrCreate(Options{
		Dir: dir, Tier: TierKeystore,
		KeystoreKEK: func() ([]byte, error) { return make([]byte, kekLen), nil },
	})
	if !errors.Is(err, ErrTierMismatch) {
		t.Errorf("reload at tier keystore = %v, want ErrTierMismatch", err)
	}
}

func TestTierKeystoreRequiresABinding(t *testing.T) {
	// M1 ships no platform keystore or TPM binding. Requesting tier 1 without
	// supplying one must fail loudly rather than fall back to a weaker tier.
	if _, err := LoadOrCreate(Options{Dir: t.TempDir(), Tier: TierKeystore}); !errors.Is(err, ErrNoKeystore) {
		t.Fatalf("tier keystore with no binding = %v, want ErrNoKeystore", err)
	}

	dir := t.TempDir()
	kek := make([]byte, kekLen)
	if _, err := rand.Read(kek); err != nil {
		t.Fatal(err)
	}
	opts := Options{
		Dir:         dir,
		Tier:        TierKeystore,
		KeystoreKEK: func() ([]byte, error) { return kek, nil },
	}
	id, err := LoadOrCreate(opts)
	if err != nil {
		t.Fatalf("LoadOrCreate at tier keystore: %v", err)
	}
	if id.PrivilegePublic() == nil {
		t.Fatal("tier keystore produced no privilege key")
	}
	reloaded, err := LoadOrCreate(opts)
	if err != nil {
		t.Fatal(err)
	}
	if !reloaded.PrivilegePublic().Equal(id.PrivilegePublic()) {
		t.Error("privilege key changed across reload at tier keystore")
	}
	priv, err := reloaded.openPrivilege(nil, kek)
	if err != nil {
		t.Fatalf("openPrivilege with the keystore KEK: %v", err)
	}
	if !matchesPublic(priv, id.PrivilegePublic()) {
		t.Error("keystore-sealed key does not match its public half")
	}
}

func TestLoadOrCreateRejectsBadInput(t *testing.T) {
	if _, err := LoadOrCreate(Options{Dir: ""}); err == nil {
		t.Error("LoadOrCreate accepted an empty directory")
	}
	if _, err := LoadOrCreate(Options{Dir: t.TempDir(), Tier: TierPassphrase}); err == nil {
		t.Error("LoadOrCreate sealed a privilege key with no passphrase")
	}
}

func TestLoadOrCreateRejectsCorruptIdentityKey(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, identityKeyFile), []byte("not a key"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Regenerating over an unreadable identity key would unpair every peer
	// without saying so; failing is the only safe response.
	if _, err := LoadOrCreate(Options{Dir: dir, Tier: TierNone}); err == nil {
		t.Fatal("LoadOrCreate accepted a corrupt identity key file")
	}
}

func TestOwnedSigningInput(t *testing.T) {
	nonce := make([]byte, OwnedNonceLen)
	for i := range nonce {
		nonce[i] = byte(i)
	}
	const target = DeviceID("kzdvvj2umnduyauf")

	got, err := OwnedSigningInput(target, 3, 0x0102, nonce, 0x1122334455667788)
	if err != nil {
		t.Fatal(err)
	}

	// PROTOCOL.md §6:
	//   signed = "openair-owned-v1" || target_device_id || capID || msgType
	//         || nonce || issued_at
	// with §0's little-endian integers.
	want := []byte("openair-owned-v1")
	want = append(want, target...)
	want = append(want, 3, 0x02, 0x01)
	want = append(want, nonce...)
	want = append(want, 0x88, 0x77, 0x66, 0x55, 0x44, 0x33, 0x22, 0x11)
	if !bytes.Equal(got, want) {
		t.Fatalf("OwnedSigningInput =\n%x\nwant\n%x", got, want)
	}
	if len(got) != 16+DeviceIDLen+1+2+OwnedNonceLen+8 {
		t.Errorf("length = %d, want %d", len(got), 16+DeviceIDLen+1+2+OwnedNonceLen+8)
	}

	// Binding: changing any field changes the signed bytes, which is what stops
	// a proof for one operation or one peer being replayed as another.
	other, err := OwnedSigningInput("copdsqhgjnkjc4ra", 3, 0x0102, nonce, 0x1122334455667788)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(got, other) {
		t.Error("changing the target device did not change the signed bytes")
	}
	other, err = OwnedSigningInput(target, 4, 0x0102, nonce, 0x1122334455667788)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(got, other) {
		t.Error("changing capID did not change the signed bytes")
	}
	other, err = OwnedSigningInput(target, 3, 0x0103, nonce, 0x1122334455667788)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(got, other) {
		t.Error("changing msgType did not change the signed bytes")
	}
}

func TestOwnedSigningInputRejectsBadInput(t *testing.T) {
	nonce := make([]byte, OwnedNonceLen)
	if _, err := OwnedSigningInput("too-short", 1, 1, nonce, 0); err == nil {
		t.Error("accepted a malformed target DeviceID")
	}
	if _, err := OwnedSigningInput("kzdvvj2umnduyauf", 1, 1, make([]byte, 8), 0); err == nil {
		t.Error("accepted an 8-byte nonce; §6 requires 32")
	}
}

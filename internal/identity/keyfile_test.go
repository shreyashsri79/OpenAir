package identity

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"testing"
)

func TestSealedKeyFileLayout(t *testing.T) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	blob, err := sealPrivilegeKey(priv, kdfArgon2id, testArgon2, []byte("passphrase"), nil)
	if err != nil {
		t.Fatalf("sealPrivilegeKey: %v", err)
	}

	// PROTOCOL.md Appendix A, field by field. Integers little-endian per §0.
	if got := string(blob[0:6]); got != "OAKEY\x00" {
		t.Errorf("magic = %q, want %q", got, "OAKEY\x00")
	}
	if blob[6] != keyFileVersion {
		t.Errorf("version = %d, want %d", blob[6], keyFileVersion)
	}
	if blob[7] != kdfArgon2id {
		t.Errorf("kdf = %d, want %d (Argon2id)", blob[7], kdfArgon2id)
	}
	if got := binary.LittleEndian.Uint32(blob[8:12]); got != testArgon2.Time {
		t.Errorf("argon_time = %d, want %d", got, testArgon2.Time)
	}
	if got := binary.LittleEndian.Uint32(blob[12:16]); got != testArgon2.Memory {
		t.Errorf("argon_memory = %d, want %d", got, testArgon2.Memory)
	}
	if blob[16] != testArgon2.Lanes {
		t.Errorf("argon_lanes = %d, want %d", blob[16], testArgon2.Lanes)
	}
	if int(blob[17]) != saltLen {
		t.Errorf("salt_len = %d, want %d", blob[17], saltLen)
	}
	off := headerFixedLen + saltLen + nonceLen
	ctLen := int(binary.LittleEndian.Uint32(blob[off : off+4]))
	if want := len(blob) - off - 4; ctLen != want {
		t.Errorf("ct_len = %d, want %d", ctLen, want)
	}

	// The sealed private key must not be recoverable by reading the file.
	if bytes.Contains(blob, priv.Seed()) {
		t.Fatal("the container holds the private key seed in the clear")
	}
}

func TestSealedKeyFileRoundTrip(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	pass := []byte("four random diceware words here")

	blob, err := sealPrivilegeKey(priv, kdfArgon2id, testArgon2, pass, nil)
	if err != nil {
		t.Fatal(err)
	}
	f, err := parseSealedKeyFile(blob)
	if err != nil {
		t.Fatalf("parseSealedKeyFile: %v", err)
	}
	if f.params != testArgon2 {
		t.Errorf("parsed params = %+v, want %+v", f.params, testArgon2)
	}
	got, err := f.open(pass, nil)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if !got.Equal(priv) {
		t.Error("opened key differs from the sealed key")
	}
	if !matchesPublic(got, pub) {
		t.Error("opened key does not match its public half")
	}
}

func TestSealedKeyFileWrongPassphrase(t *testing.T) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	blob, err := sealPrivilegeKey(priv, kdfArgon2id, testArgon2, []byte("right"), nil)
	if err != nil {
		t.Fatal(err)
	}
	f, err := parseSealedKeyFile(blob)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.open([]byte("wrong"), nil); !errors.Is(err, ErrPassphrase) {
		t.Fatalf("open with a wrong passphrase = %v, want ErrPassphrase", err)
	}
}

// TestSealedKeyFileHeaderIsAuthenticated is Appendix A's stated reason for
// making the header associated data: KDF parameters must not be downgradable by
// an attacker who can edit the file. Rewriting argon_time to 1 would otherwise
// let a grinder skip most of the work.
func TestSealedKeyFileHeaderIsAuthenticated(t *testing.T) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	pass := []byte("passphrase")
	params := Argon2Params{Time: 2, Memory: 16, Lanes: 1}
	blob, err := sealPrivilegeKey(priv, kdfArgon2id, params, pass, nil)
	if err != nil {
		t.Fatal(err)
	}

	tampered := bytes.Clone(blob)
	binary.LittleEndian.PutUint32(tampered[8:12], 1) // argon_time: 2 -> 1
	f, err := parseSealedKeyFile(tampered)
	if err != nil {
		t.Fatalf("parseSealedKeyFile on a downgraded header: %v", err)
	}
	if f.params.Time != 1 {
		t.Fatalf("tamper did not take: argon_time = %d", f.params.Time)
	}
	if _, err := f.open(pass, nil); !errors.Is(err, ErrPassphrase) {
		t.Fatalf("downgraded header opened with err = %v, want ErrPassphrase", err)
	}

	// Same for the salt, which is inside the associated data too.
	tampered = bytes.Clone(blob)
	tampered[headerFixedLen] ^= 0xff
	f, err = parseSealedKeyFile(tampered)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.open(pass, nil); !errors.Is(err, ErrPassphrase) {
		t.Fatalf("tampered salt opened with err = %v, want ErrPassphrase", err)
	}
}

func TestSealedKeyFileCiphertextTamper(t *testing.T) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	pass := []byte("passphrase")
	blob, err := sealPrivilegeKey(priv, kdfArgon2id, testArgon2, pass, nil)
	if err != nil {
		t.Fatal(err)
	}
	blob[len(blob)-1] ^= 0xff
	f, err := parseSealedKeyFile(blob)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.open(pass, nil); !errors.Is(err, ErrPassphrase) {
		t.Fatalf("tampered ciphertext opened with err = %v, want ErrPassphrase", err)
	}
}

func TestSealedKeyFileKeystoreKDF(t *testing.T) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	kek := make([]byte, kekLen)
	if _, err := rand.Read(kek); err != nil {
		t.Fatal(err)
	}

	blob, err := sealPrivilegeKey(priv, kdfNone, DefaultArgon2Params, nil, kek)
	if err != nil {
		t.Fatalf("sealPrivilegeKey kdf=none: %v", err)
	}
	f, err := parseSealedKeyFile(blob)
	if err != nil {
		t.Fatal(err)
	}
	// Appendix A: with kdf = 0 no passphrase material is present.
	if f.params != (Argon2Params{}) {
		t.Errorf("kdf=none carried Argon2id parameters %+v", f.params)
	}
	if len(f.salt) != 0 {
		t.Errorf("kdf=none carried a %d-byte salt", len(f.salt))
	}
	got, err := f.open(nil, kek)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if !got.Equal(priv) {
		t.Error("opened key differs from the sealed key")
	}

	wrong := make([]byte, kekLen)
	if _, err := f.open(nil, wrong); !errors.Is(err, ErrPassphrase) {
		t.Fatalf("open with the wrong keystore KEK = %v, want ErrPassphrase", err)
	}
	if _, err := f.open(nil, nil); !errors.Is(err, ErrNoKeystore) {
		t.Fatalf("open with no keystore KEK = %v, want ErrNoKeystore", err)
	}
}

func TestParseSealedKeyFileRejectsMalformed(t *testing.T) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	good, err := sealPrivilegeKey(priv, kdfArgon2id, testArgon2, []byte("p"), nil)
	if err != nil {
		t.Fatal(err)
	}

	mutate := func(f func([]byte) []byte) []byte { return f(bytes.Clone(good)) }

	cases := []struct {
		name string
		in   []byte
	}{
		{"empty", nil},
		{"short", good[:10]},
		{"truncated ciphertext", good[:len(good)-4]},
		{"bad magic", mutate(func(b []byte) []byte { b[0] = 'X'; return b })},
		{"bad version", mutate(func(b []byte) []byte { b[6] = 99; return b })},
		{"unknown kdf", mutate(func(b []byte) []byte { b[7] = 7; return b })},
		{"zero argon params with argon2id", mutate(func(b []byte) []byte {
			binary.LittleEndian.PutUint32(b[8:12], 0)
			return b
		})},
		{"ct_len disagrees with the file", mutate(func(b []byte) []byte {
			off := headerFixedLen + saltLen + nonceLen
			binary.LittleEndian.PutUint32(b[off:off+4], 9999)
			return b
		})},
		{"trailing garbage", append(bytes.Clone(good), 0, 0, 0)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := parseSealedKeyFile(c.in); !errors.Is(err, ErrKeyFileFormat) {
				t.Fatalf("parseSealedKeyFile = %v, want ErrKeyFileFormat", err)
			}
		})
	}
}

func TestSealPrivilegeKeyRejectsBadInput(t *testing.T) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sealPrivilegeKey(priv, kdfArgon2id, testArgon2, nil, nil); err == nil {
		t.Error("sealed an Argon2id container with no passphrase")
	}
	if _, err := sealPrivilegeKey(priv, kdfArgon2id, Argon2Params{}, []byte("p"), nil); err == nil {
		t.Error("sealed an Argon2id container with zero cost parameters")
	}
	if _, err := sealPrivilegeKey(priv, kdfNone, testArgon2, nil, []byte("short")); !errors.Is(err, ErrNoKeystore) {
		t.Errorf("sealPrivilegeKey with a short keystore KEK = %v, want ErrNoKeystore", err)
	}
}

func TestMatchesPublic(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	other, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if !matchesPublic(priv, pub) {
		t.Error("matchesPublic rejected a matching pair")
	}
	if matchesPublic(priv, other) {
		t.Error("matchesPublic accepted a mismatched pair")
	}
	if matchesPublic(priv, nil) {
		t.Error("matchesPublic accepted a nil public key")
	}
}

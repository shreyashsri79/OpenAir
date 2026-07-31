package identity

import (
	"crypto/ed25519"
	"encoding/hex"
	"testing"
)

// Fixed vectors for PROTOCOL.md §2:
//
//	DeviceID = base32( SHA-256( identity_public_key )[0:10] )   // 16 characters
//
// base32 is RFC 4648 lowercase, no padding (§0). The expected values were
// computed independently of this package (Python: base64.b32encode of
// hashlib.sha256(pub).digest()[:10], lowercased, padding stripped) so the test
// checks the formula rather than checking DeriveDeviceID against itself.
var deviceIDVectors = []struct {
	name   string
	seed   string // hex, 32 bytes -- the Ed25519 seed
	pub    string // hex, 32 bytes -- the derived public key
	device DeviceID
}{
	{
		name:   "seed 00..1f",
		seed:   "000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f",
		pub:    "03a107bff3ce10be1d70dd18e74bc09967e4d6309ba50d5f1ddc8664125531b8",
		device: "kzdvvj2umnduyauf",
	},
	{
		name:   "zero seed",
		seed:   "0000000000000000000000000000000000000000000000000000000000000000",
		pub:    "3b6a27bcceb6a42d62a3a8d02a6f0d73653215771de243a63ac048a18b59da29",
		device: "copdsqhgjnkjc4ra",
	},
	{
		// The seed the D-3 spike uses in oabench/bench/tlsutil.go
		// ("openair-v2-spike-fixed-seed-32byt"), so that the ported
		// implementation is pinned to the measured one byte for byte.
		name:   "oabench spike seed",
		seed:   "6f70656e6169722d76322d7370696b652d66697865642d736565642d3332627974",
		pub:    "e4b3bedda55ed2ca62c003f4da89f8f2025df6bda79b13915e3172c0f08449ca",
		device: "jpdbyv3tyysaqb7l",
	},
}

func TestDeriveDeviceIDVectors(t *testing.T) {
	for _, v := range deviceIDVectors {
		t.Run(v.name, func(t *testing.T) {
			seed, err := hex.DecodeString(v.seed)
			if err != nil {
				t.Fatalf("bad seed vector: %v", err)
			}
			priv := ed25519.NewKeyFromSeed(seed[:ed25519.SeedSize])
			pub := priv.Public().(ed25519.PublicKey)
			if got := hex.EncodeToString(pub); got != v.pub {
				t.Fatalf("public key = %s, want %s", got, v.pub)
			}
			got := DeriveDeviceID(pub)
			if got != v.device {
				t.Errorf("DeriveDeviceID = %q, want %q", got, v.device)
			}
			if len(got) != DeviceIDLen {
				t.Errorf("DeviceID length = %d, want %d", len(got), DeviceIDLen)
			}
			if !got.Valid() {
				t.Errorf("DeriveDeviceID produced %q, which Valid rejects", got)
			}
		})
	}
}

func TestDeriveDeviceIDIsStable(t *testing.T) {
	pub, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	first := DeriveDeviceID(pub)
	for i := 0; i < 10; i++ {
		if got := DeriveDeviceID(pub); got != first {
			t.Fatalf("DeriveDeviceID is not deterministic: %q then %q", first, got)
		}
	}
	// A one-bit change in the key must change the ID.
	other := make(ed25519.PublicKey, len(pub))
	copy(other, pub)
	other[0] ^= 1
	if DeriveDeviceID(other) == first {
		t.Fatal("flipping a key bit did not change the DeviceID")
	}
}

func TestDeviceIDValid(t *testing.T) {
	cases := []struct {
		in   DeviceID
		want bool
	}{
		{"kzdvvj2umnduyauf", true},
		{"", false},
		{"kzdvvj2umnduyau", false},   // 15 characters
		{"kzdvvj2umnduyaufg", false}, // 17 characters
		{"KZDVVJ2UMNDUYAUF", false},  // §2 requires lowercase
		{"kzdvvj2umnduyau=", false},  // §0 requires no padding
		{"kzdvvj2umnduya01", false},  // 0 and 1 are not in the base32 alphabet
	}
	for _, c := range cases {
		if got := c.in.Valid(); got != c.want {
			t.Errorf("DeviceID(%q).Valid() = %v, want %v", c.in, got, c.want)
		}
	}
}

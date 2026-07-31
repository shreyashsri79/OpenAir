package pairing

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"strings"
	"testing"

	"github.com/shreyashsri79/openair/internal/identity"
	v1 "github.com/shreyashsri79/openair/internal/wire/openair/v1"
)

// offerFor builds the offer a device holding pub would display, without
// needing a whole identity.
func offerFor(t *testing.T, pub ed25519.PublicKey, hints ...string) *v1.PairOffer {
	t.Helper()
	return &v1.PairOffer{
		DeviceId:            string(identity.DeriveDeviceID(pub)),
		IdentityFingerprint: Fingerprint(pub),
		LanHints:            hints,
		ProtoVersion:        ProtoVersion,
	}
}

func TestOffer_RoundTrip(t *testing.T) {
	pub := mustKey(t)
	want := offerFor(t, pub, "10.0.0.5:9000", "[fe80::1]:9000")

	encoded, err := EncodeOffer(want)
	if err != nil {
		t.Fatalf("EncodeOffer: %v", err)
	}
	if !strings.HasPrefix(encoded, OfferScheme) {
		t.Fatalf("encoded offer %q does not start with %q", encoded, OfferScheme)
	}

	got, err := DecodeOffer(encoded)
	if err != nil {
		t.Fatalf("DecodeOffer: %v", err)
	}
	if got.DeviceId != want.DeviceId {
		t.Fatalf("device id round-tripped as %q, want %q", got.DeviceId, want.DeviceId)
	}
	if string(got.IdentityFingerprint) != string(want.IdentityFingerprint) {
		t.Fatal("fingerprint did not survive the round trip")
	}
	if len(got.LanHints) != 2 || got.LanHints[0] != "10.0.0.5:9000" {
		t.Fatalf("LAN hints round-tripped as %v", got.LanHints)
	}
}

// The manual-entry form is what a user retypes when there is no camera, so
// decoding has to tolerate the case, spacing and hyphenation they will produce.
func TestDecodeOffer_ToleratesHumanTyping(t *testing.T) {
	pub := mustKey(t)
	grouped, err := EncodeOfferGrouped(offerFor(t, pub))
	if err != nil {
		t.Fatalf("EncodeOfferGrouped: %v", err)
	}
	if !strings.Contains(grouped, "-") {
		t.Fatalf("grouped form %q has no separators", grouped)
	}

	want := identity.DeriveDeviceID(pub)
	for _, form := range []string{
		grouped,
		strings.ToUpper(grouped),
		strings.ReplaceAll(grouped, "-", " "),
		strings.ReplaceAll(grouped, "-", ""),
		"  " + grouped + "\n",
		OfferScheme + strings.ReplaceAll(grouped, "-", ""),
	} {
		got, err := DecodeOffer(form)
		if err != nil {
			t.Fatalf("DecodeOffer(%q): %v", form, err)
		}
		if identity.DeviceID(got.DeviceId) != want {
			t.Fatalf("DecodeOffer(%q) gave device %q, want %q", form, got.DeviceId, want)
		}
	}
}

func TestDecodeOffer_RejectsGarbage(t *testing.T) {
	for _, s := range []string{"", "   ", OfferScheme, "openair://pair/!!!!", "notanoffer"} {
		if _, err := DecodeOffer(s); err == nil {
			t.Fatalf("DecodeOffer(%q) accepted garbage", s)
		}
	}
}

func TestVerifyOffer_MatchingKeyPasses(t *testing.T) {
	pub := mustKey(t)
	if err := VerifyOffer(offerFor(t, pub), pub); err != nil {
		t.Fatalf("VerifyOffer rejected the key the offer was built from: %v", err)
	}
}

// §5.1's check: the device answering must be the device whose code was scanned.
// A mismatch means something else is on the wire, so it must never be
// retryable -- backing off and dialling again would hide the re-pair prompt.
func TestVerifyOffer_WrongKeyIsAHardFailure(t *testing.T) {
	offered := mustKey(t)
	answering := mustKey(t)

	err := VerifyOffer(offerFor(t, offered), answering)
	if err == nil {
		t.Fatal("VerifyOffer accepted a key that is not the one in the offer")
	}
	if !errors.Is(err, identity.ErrKeyMismatch) {
		t.Fatalf("error does not classify as a key mismatch: %v", err)
	}
	if Retryable(err) {
		t.Fatal("an offer answered by the wrong device was reported retryable")
	}

	var mismatch *OfferMismatchError
	if !errors.As(err, &mismatch) {
		t.Fatalf("error is not an *OfferMismatchError: %v", err)
	}
	if mismatch.Offered != identity.DeriveDeviceID(offered) || mismatch.Presented != identity.DeriveDeviceID(answering) {
		t.Fatalf("mismatch names the wrong devices: %+v", mismatch)
	}
}

// The fingerprint and the DeviceID are two truncations of the same digest. An
// offer where they disagree is internally inconsistent, and every later lookup
// keyed by that ID would be wrong.
func TestVerifyOffer_RejectsInconsistentOffer(t *testing.T) {
	pub := mustKey(t)
	other := mustKey(t)

	o := offerFor(t, pub)
	o.DeviceId = string(identity.DeriveDeviceID(other))

	if err := VerifyOffer(o, pub); err == nil {
		t.Fatal("VerifyOffer accepted an offer whose device id does not derive from its fingerprint")
	}
}

func TestVerifyOffer_RejectsUnknownProtocolVersion(t *testing.T) {
	pub := mustKey(t)
	o := offerFor(t, pub)
	o.ProtoVersion = ProtoVersion + 1
	if err := VerifyOffer(o, pub); err == nil {
		t.Fatal("VerifyOffer accepted an offer from a protocol version this build does not speak")
	}
}

func TestNewOffer_FromIdentity(t *testing.T) {
	id, err := identity.LoadOrCreate(identity.Options{Dir: t.TempDir(), Tier: identity.TierNone})
	if err != nil {
		t.Fatalf("LoadOrCreate: %v", err)
	}
	o, err := NewOffer(id, []string{"127.0.0.1:9000"})
	if err != nil {
		t.Fatalf("NewOffer: %v", err)
	}
	if identity.DeviceID(o.DeviceId) != id.DeviceID() {
		t.Fatalf("offer device id %q is not the identity's %q", o.DeviceId, id.DeviceID())
	}
	if err := VerifyOffer(o, id.IdentityPublic()); err != nil {
		t.Fatalf("an offer built from an identity does not verify against it: %v", err)
	}
}

func TestFingerprint_IsTheDocumentedTruncation(t *testing.T) {
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	if got := Fingerprint(pub); len(got) != FingerprintLen {
		t.Fatalf("fingerprint is %d bytes, want %d", len(got), FingerprintLen)
	}
}

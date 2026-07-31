package pairing

import (
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base32"
	"fmt"
	"strings"

	"google.golang.org/protobuf/proto"

	"github.com/shreyashsri79/openair/internal/identity"
	v1 "github.com/shreyashsri79/openair/internal/wire/openair/v1"
)

// FingerprintLen is the length of PairOffer.identity_fingerprint:
// SHA-256(identity_public_key)[0:16] (PROTOCOL.md §5.1).
//
// Note it is a different truncation of the same digest as the DeviceID, which
// takes the first 10 bytes. The offer carries both, and they must agree.
const FingerprintLen = 16

// OfferScheme prefixes the encoded offer so a scanned string can be recognised
// as an OpenAir pairing offer rather than a URL, and so a QR reader that hands
// it to the OS opens the right application.
const OfferScheme = "openair://pair/"

// offerEncoding is RFC 4648 base32 without padding (PROTOCOL.md §0), the same
// alphabet as the DeviceID. Base32 rather than base64 because the payload is
// typed by hand when there is no camera: it has no case distinction to get
// wrong and no characters a terminal or a URL will mangle.
var offerEncoding = base32.StdEncoding.WithPadding(base32.NoPadding)

// offerGroup is how many characters go between hyphens in the manual-entry
// form. Hyphens are stripped on decode and carry no meaning.
const offerGroup = 8

// ProtoVersion is the pairing protocol version advertised in the offer. It is
// the same version the session negotiates in Hello (PROTOCOL.md §4).
const ProtoVersion uint32 = 1

// Fingerprint returns SHA-256(pub)[0:16], the value carried in a PairOffer
// (PROTOCOL.md §5.1).
func Fingerprint(pub ed25519.PublicKey) []byte {
	sum := sha256.Sum256(pub)
	out := make([]byte, FingerprintLen)
	copy(out, sum[:FingerprintLen])
	return out
}

// NewOffer builds the out-of-band offer this device displays (§5.1). hints are
// "host:port" candidates the scanning device may dial; they are advisory and an
// offer with none is still usable when the address is known by other means.
func NewOffer(local identity.Identity, hints []string) (*v1.PairOffer, error) {
	pub := local.IdentityPublic()
	if len(pub) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("pairing: local identity key is %d bytes, want %d", len(pub), ed25519.PublicKeySize)
	}
	return &v1.PairOffer{
		DeviceId:            string(local.DeviceID()),
		IdentityFingerprint: Fingerprint(pub),
		LanHints:            append([]string(nil), hints...),
		ProtoVersion:        ProtoVersion,
	}, nil
}

// EncodeOffer renders an offer as one printable string. The same string is what
// a QR code carries and what a user types when there is no camera.
func EncodeOffer(o *v1.PairOffer) (string, error) {
	if err := validateOffer(o); err != nil {
		return "", err
	}
	b, err := proto.Marshal(o)
	if err != nil {
		return "", fmt.Errorf("pairing: marshal offer: %w", err)
	}
	return OfferScheme + strings.ToLower(offerEncoding.EncodeToString(b)), nil
}

// EncodeOfferGrouped renders an offer for manual entry, hyphenated every eight
// characters and without the scheme prefix. DecodeOffer accepts either form.
func EncodeOfferGrouped(o *v1.PairOffer) (string, error) {
	s, err := EncodeOffer(o)
	if err != nil {
		return "", err
	}
	body := strings.TrimPrefix(s, OfferScheme)
	var b strings.Builder
	for i := 0; i < len(body); i += offerGroup {
		if i > 0 {
			b.WriteByte('-')
		}
		b.WriteString(body[i:min(i+offerGroup, len(body))])
	}
	return b.String(), nil
}

// DecodeOffer parses either form produced above, tolerating the case, spacing
// and hyphenation a human retyping it will introduce.
func DecodeOffer(s string) (*v1.PairOffer, error) {
	body := strings.TrimSpace(s)
	if i := strings.Index(strings.ToLower(body), OfferScheme); i == 0 {
		body = body[len(OfferScheme):]
	}
	body = strings.Map(func(r rune) rune {
		switch r {
		case '-', ' ', '\t', '\n', '\r':
			return -1
		}
		return r
	}, body)
	if body == "" {
		return nil, fmt.Errorf("pairing: empty pairing offer")
	}
	raw, err := offerEncoding.DecodeString(strings.ToUpper(body))
	if err != nil {
		return nil, fmt.Errorf("pairing: decode offer: %w", err)
	}
	var o v1.PairOffer
	if err := proto.Unmarshal(raw, &o); err != nil {
		return nil, fmt.Errorf("pairing: parse offer: %w", err)
	}
	if err := validateOffer(&o); err != nil {
		return nil, err
	}
	return &o, nil
}

// VerifyOffer checks the key a peer actually presented in TLS against the offer
// that was scanned out of band (PROTOCOL.md §5.1: "B MUST verify that A's
// presented TLS key matches identity_fingerprint before proceeding").
//
// This is the half of pairing the SAS does not cover: the offer authenticates A
// to B, the SAS authenticates B to A. A mismatch is not a transient failure --
// it means the device answering is not the device whose code was scanned -- so
// the error wraps identity.ErrKeyMismatch and Retryable reports false.
func VerifyOffer(o *v1.PairOffer, presented ed25519.PublicKey) error {
	if err := validateOffer(o); err != nil {
		return err
	}
	if len(presented) != ed25519.PublicKeySize {
		return fmt.Errorf("pairing: presented key is %d bytes, want %d", len(presented), ed25519.PublicKeySize)
	}
	if o.ProtoVersion != ProtoVersion {
		return fmt.Errorf("pairing: offer advertises protocol version %d, this build speaks %d",
			o.ProtoVersion, ProtoVersion)
	}
	got := Fingerprint(presented)
	if subtle.ConstantTimeCompare(got, o.IdentityFingerprint) != 1 {
		return &OfferMismatchError{
			Offered:   identity.DeviceID(o.DeviceId),
			Presented: identity.DeriveDeviceID(presented),
		}
	}
	// The fingerprint matched, so the DeviceID in the offer must derive from the
	// same key. If it does not, the offer is internally inconsistent and every
	// later lookup keyed by that ID would be wrong.
	if got := identity.DeriveDeviceID(presented); got != identity.DeviceID(o.DeviceId) {
		return &OfferMismatchError{Offered: identity.DeviceID(o.DeviceId), Presented: got}
	}
	return nil
}

func validateOffer(o *v1.PairOffer) error {
	if o == nil {
		return fmt.Errorf("pairing: nil offer")
	}
	if !identity.DeviceID(o.DeviceId).Valid() {
		return fmt.Errorf("pairing: offer device_id %q is not a DeviceID", o.DeviceId)
	}
	if len(o.IdentityFingerprint) != FingerprintLen {
		return fmt.Errorf("pairing: offer fingerprint is %d bytes, want %d",
			len(o.IdentityFingerprint), FingerprintLen)
	}
	return nil
}

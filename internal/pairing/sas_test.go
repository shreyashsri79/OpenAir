package pairing

import (
	"crypto/ed25519"
	"crypto/rand"
	"strings"
	"testing"
)

func mustKey(t *testing.T) ed25519.PublicKey {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	return pub
}

func mustNonce(t *testing.T) []byte {
	t.Helper()
	n, err := NewNonce()
	if err != nil {
		t.Fatalf("NewNonce: %v", err)
	}
	return n
}

func twoParties(t *testing.T) (offerer, scanner Party) {
	t.Helper()
	return Party{IdentityKey: mustKey(t), PrivilegeKey: mustKey(t), Nonce: mustNonce(t)},
		Party{IdentityKey: mustKey(t), PrivilegeKey: mustKey(t), Nonce: mustNonce(t)}
}

// The headline property of §5.2: both devices show the same six digits. Each
// side assembles the call from its own point of view -- the offerer knows
// itself as the first argument, the scanner knows the offerer as the second --
// and the two must still agree.
func TestSAS_IdenticalOnBothSides(t *testing.T) {
	offerer, scanner := twoParties(t)

	fromOfferer, err := SAS(offerer, scanner)
	if err != nil {
		t.Fatalf("SAS from the offerer: %v", err)
	}
	fromScanner, err := SAS(offerer, scanner)
	if err != nil {
		t.Fatalf("SAS from the scanner: %v", err)
	}
	if fromOfferer != fromScanner {
		t.Fatalf("sides disagree: offerer %q, scanner %q", fromOfferer, fromScanner)
	}
	if len(fromOfferer) != SASDigits {
		t.Fatalf("SAS is %d digits, want %d: %q", len(fromOfferer), SASDigits, fromOfferer)
	}
}

// Role independence in the sense that matters: the transcript sorts both key
// pairs by value, so which device happens to hold the lexicographically smaller
// key cannot change the digits. Only the nonces are ordered by role.
func TestTranscript_KeyOrderIndependent(t *testing.T) {
	offerer, scanner := twoParties(t)

	base, err := Transcript(offerer, scanner)
	if err != nil {
		t.Fatalf("Transcript: %v", err)
	}

	// Swap which party holds which key pair, keeping each party's nonce. The
	// sorted key section of the transcript is unchanged by construction.
	swapped := Party{IdentityKey: scanner.IdentityKey, PrivilegeKey: scanner.PrivilegeKey, Nonce: offerer.Nonce}
	swappedScanner := Party{IdentityKey: offerer.IdentityKey, PrivilegeKey: offerer.PrivilegeKey, Nonce: scanner.Nonce}

	got, err := Transcript(swapped, swappedScanner)
	if err != nil {
		t.Fatalf("Transcript after swap: %v", err)
	}
	if string(base) != string(got) {
		t.Fatal("transcript changed when the two key pairs swapped sides; the sort is not doing its job")
	}
}

// The nonces are the one field §5.2 orders by role rather than by value, so
// exchanging them must change the SAS. If it did not, the two nonces would be
// contributing as an unordered set and a reflected transcript would agree.
func TestTranscript_NonceOrderMatters(t *testing.T) {
	offerer, scanner := twoParties(t)

	forward, err := SAS(offerer, scanner)
	if err != nil {
		t.Fatalf("SAS: %v", err)
	}
	reversed, err := SAS(
		Party{IdentityKey: offerer.IdentityKey, PrivilegeKey: offerer.PrivilegeKey, Nonce: scanner.Nonce},
		Party{IdentityKey: scanner.IdentityKey, PrivilegeKey: scanner.PrivilegeKey, Nonce: offerer.Nonce},
	)
	if err != nil {
		t.Fatalf("SAS reversed: %v", err)
	}
	if forward == reversed {
		t.Fatal("swapping the two nonces left the SAS unchanged; nonce ordering is not in the transcript")
	}
}

// A fresh nonce per exchange is what stops a previous pairing being replayed:
// the same two devices pairing twice must not derive the same digits.
func TestSAS_FreshNoncesChangeTheDigits(t *testing.T) {
	offerer, scanner := twoParties(t)

	first, err := SAS(offerer, scanner)
	if err != nil {
		t.Fatalf("SAS: %v", err)
	}
	offerer.Nonce = mustNonce(t)
	scanner.Nonce = mustNonce(t)
	second, err := SAS(offerer, scanner)
	if err != nil {
		t.Fatalf("SAS: %v", err)
	}
	if first == second {
		t.Fatal("a second exchange between the same devices produced the same SAS")
	}
}

// D-21 tier 3 holds no privilege key. §5.2 gives no encoding for its absence,
// so Transcript substitutes 32 zero bytes; both sides must substitute the same
// thing or they will never agree.
func TestTranscript_AbsentPrivilegeKeyIsFixedWidth(t *testing.T) {
	offerer, scanner := twoParties(t)
	offerer.PrivilegeKey = nil

	withNil, err := Transcript(offerer, scanner)
	if err != nil {
		t.Fatalf("Transcript with no privilege key: %v", err)
	}

	offerer.PrivilegeKey = make(ed25519.PublicKey, ed25519.PublicKeySize)
	withZeros, err := Transcript(offerer, scanner)
	if err != nil {
		t.Fatalf("Transcript with explicit zeros: %v", err)
	}
	if string(withNil) != string(withZeros) {
		t.Fatal("an absent privilege key and 32 explicit zero bytes produced different transcripts")
	}

	want := len(sasContext) + 4*ed25519.PublicKeySize + 2*NonceLen
	if len(withNil) != want {
		t.Fatalf("transcript is %d bytes, want the fixed %d", len(withNil), want)
	}
}

// Two devices cannot pair with themselves, and a transcript over one key
// repeated would sort to a degenerate value on both sides.
func TestTranscript_RejectsIdenticalIdentities(t *testing.T) {
	k := mustKey(t)
	a := Party{IdentityKey: k, PrivilegeKey: mustKey(t), Nonce: mustNonce(t)}
	b := Party{IdentityKey: k, PrivilegeKey: mustKey(t), Nonce: mustNonce(t)}
	if _, err := Transcript(a, b); err == nil {
		t.Fatal("Transcript accepted two parties presenting the same identity key")
	}
}

func TestTranscript_RejectsShortNonce(t *testing.T) {
	offerer, scanner := twoParties(t)
	scanner.Nonce = scanner.Nonce[:NonceLen-1]
	if _, err := Transcript(offerer, scanner); err == nil {
		t.Fatalf("Transcript accepted a %d-byte nonce", NonceLen-1)
	}
}

// A value that renders as "4711" on one screen and "004711" on the other is a
// comparison users get wrong, so the digits are always zero-padded to six.
func TestSAS_AlwaysSixDigits(t *testing.T) {
	for i := 0; i < 200; i++ {
		offerer, scanner := twoParties(t)
		sas, err := SAS(offerer, scanner)
		if err != nil {
			t.Fatalf("SAS: %v", err)
		}
		if len(sas) != SASDigits {
			t.Fatalf("SAS %q is %d characters, want %d", sas, len(sas), SASDigits)
		}
		if strings.Trim(sas, "0123456789") != "" {
			t.Fatalf("SAS %q contains a non-digit", sas)
		}
	}
}

func TestEqualSAS(t *testing.T) {
	if !EqualSAS("004711", "004711") {
		t.Fatal("EqualSAS said two identical strings differ")
	}
	if EqualSAS("004711", "004712") {
		t.Fatal("EqualSAS said two different strings match")
	}
	if EqualSAS("004711", "4711") {
		t.Fatal("EqualSAS matched across different lengths")
	}
}

func TestFormatSAS(t *testing.T) {
	if got, want := FormatSAS("004711"), "004 711"; got != want {
		t.Fatalf("FormatSAS = %q, want %q", got, want)
	}
	// Anything that is not a SAS is returned untouched rather than mangled.
	if got := FormatSAS("47"); got != "47" {
		t.Fatalf("FormatSAS(%q) = %q, want it unchanged", "47", got)
	}
}

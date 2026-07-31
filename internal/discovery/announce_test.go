package discovery

import (
	"errors"
	"net"
	"slices"
	"strings"
	"testing"

	"github.com/shreyashsri79/openair/internal/identity"
)

// idOf builds a syntactically valid DeviceID without needing a key.
func idOf(t *testing.T, seed byte) identity.DeviceID {
	t.Helper()
	key := make([]byte, 32)
	for i := range key {
		key[i] = seed
	}
	return identity.DeriveDeviceID(key)
}

func sampleAnnounce(t *testing.T) Announce {
	t.Helper()
	return Announce{
		DeviceID:     idOf(t, 1),
		ProtoVersion: ProtoVersion,
		Port:         9000,
		DisplayName:  "desktop-home",
	}
}

func TestTXT_RoundTrip(t *testing.T) {
	want := sampleAnnounce(t)
	got, err := ParseTXT(want.TXT())
	if err != nil {
		t.Fatalf("ParseTXT: %v", err)
	}
	if got != want {
		t.Fatalf("round trip changed the announce:\n got %+v\nwant %+v", got, want)
	}
}

// §15.1 fixes four keys today. A later version adding a fifth must not make
// this build blind to the device that sent it.
func TestParseTXT_IgnoresUnknownKeys(t *testing.T) {
	a := sampleAnnounce(t)
	txt := append(a.TXT(), "future=whatever", "malformed-no-equals")

	got, err := ParseTXT(txt)
	if err != nil {
		t.Fatalf("ParseTXT rejected a record carrying an unknown key: %v", err)
	}
	if got != a {
		t.Fatalf("unknown keys changed the parse:\n got %+v\nwant %+v", got, a)
	}
}

func TestParseTXT_RejectsUnusableRecords(t *testing.T) {
	valid := sampleAnnounce(t)
	for name, txt := range map[string][]string{
		"no id":         {"v=1", "port=9000"},
		"bad id":        {"id=not-a-device-id", "v=1", "port=9000"},
		"no port":       {"id=" + string(valid.DeviceID), "v=1"},
		"port zero":     {"id=" + string(valid.DeviceID), "v=1", "port=0"},
		"port too big":  {"id=" + string(valid.DeviceID), "v=1", "port=70000"},
		"port not int":  {"id=" + string(valid.DeviceID), "v=1", "port=quic"},
		"version zero":  {"id=" + string(valid.DeviceID), "v=0", "port=9000"},
		"version words": {"id=" + string(valid.DeviceID), "v=one", "port=9000"},
		"empty":         {},
	} {
		if _, err := ParseTXT(txt); err == nil {
			t.Errorf("%s: ParseTXT accepted %v", name, txt)
		}
	}
}

// A display name is the only field a user controls, and a single TXT string is
// capped at 255 bytes by the DNS wire format. Truncation must not split a rune.
func TestTruncateUTF8_DoesNotSplitRunes(t *testing.T) {
	long := strings.Repeat("é", 100) // two bytes per rune
	got := truncateUTF8(long, maxDisplayName)
	if len(got) > maxDisplayName {
		t.Fatalf("truncated to %d bytes, want at most %d", len(got), maxDisplayName)
	}
	if !isValidUTF8(got) {
		t.Fatalf("truncation produced invalid UTF-8: %q", got)
	}
}

func isValidUTF8(s string) bool {
	for _, r := range s {
		if r == '�' {
			return false
		}
	}
	return true
}

func TestDatagram_AnnounceRoundTrip(t *testing.T) {
	want := sampleAnnounce(t)

	d, err := decodeDatagram(EncodeAnnounce(want))
	if err != nil {
		t.Fatalf("decodeDatagram: %v", err)
	}
	if d.isQuery {
		t.Fatal("an announce decoded as a query")
	}
	if got := d.announce; got != want {
		t.Fatalf("datagram round trip changed the announce:\n got %+v\nwant %+v", got, want)
	}
}

func TestDatagram_QueryCarriesTheAsker(t *testing.T) {
	from := idOf(t, 2)

	d, err := decodeDatagram(EncodeQuery(from))
	if err != nil {
		t.Fatalf("decodeDatagram: %v", err)
	}
	if !d.isQuery {
		t.Fatal("a query decoded as an announce")
	}
	if d.queryFrom != from {
		t.Fatalf("query names %q, want %q", d.queryFrom, from)
	}
}

// Anyone can write to the fallback port. Nothing that is not an OpenAir
// datagram may parse as one, and nothing malformed may panic.
func TestDecodeDatagram_RejectsForeignTraffic(t *testing.T) {
	valid := EncodeAnnounce(sampleAnnounce(t))

	wrongMagic := slices.Clone(valid)
	copy(wrongMagic, "XXXX")

	wrongVersion := slices.Clone(valid)
	wrongVersion[4] = 99

	truncatedBody := slices.Clone(valid)
	truncatedBody = truncatedBody[:len(truncatedBody)-3]

	for name, b := range map[string][]byte{
		"empty":           {},
		"one byte":        {'O'},
		"magic only":      []byte(magic),
		"header only":     append([]byte(magic), datagramVer, typeAnnounce),
		"wrong magic":     wrongMagic,
		"wrong version":   wrongVersion,
		"unknown type":    append(append([]byte(magic), datagramVer, 99), 0),
		"truncated body":  truncatedBody,
		"random":          []byte("GET / HTTP/1.1\r\n\r\n"),
		"length overruns": append([]byte(magic), datagramVer, typeAnnounce, 200, 'a'),
	} {
		if _, err := decodeDatagram(b); err == nil {
			t.Errorf("%s: decodeDatagram accepted %q", name, b)
		}
	}

	if _, err := decodeDatagram([]byte("XXXX")); !errors.Is(err, ErrWrongMagic) &&
		!errors.Is(err, ErrMalformedAnnounce) {
		t.Errorf("foreign traffic produced an unclassified error: %v", err)
	}
}

// Addresses are ranked so the connection manager tries the most likely path
// first: a private LAN address beats a link-local one, and loopback is last
// because it only ever works when both ends are this machine.
func TestRankAddrs_PrefersRoutableAddresses(t *testing.T) {
	got := rankAddrs([]net.IP{
		net.ParseIP("127.0.0.1"),
		net.ParseIP("169.254.10.1"),
		net.ParseIP("192.168.1.5"),
	}, 9000)

	want := []string{"192.168.1.5:9000", "169.254.10.1:9000", "127.0.0.1:9000"}
	if !slices.Equal(got, want) {
		t.Fatalf("rankAddrs = %v, want %v", got, want)
	}
}

func TestRankAddrs_FormatsIPv6WithBrackets(t *testing.T) {
	got := rankAddrs([]net.IP{net.ParseIP("fd00::1")}, 9000)
	if len(got) != 1 || got[0] != "[fd00::1]:9000" {
		t.Fatalf("rankAddrs = %v, want a bracketed IPv6 host:port", got)
	}
	// And it has to survive the round trip through the resolver, since every
	// consumer of a candidate hands these straight to a dialler.
	if _, _, err := net.SplitHostPort(got[0]); err != nil {
		t.Fatalf("SplitHostPort(%q): %v", got[0], err)
	}
}

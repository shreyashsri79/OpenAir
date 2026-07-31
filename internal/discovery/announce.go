package discovery

import (
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"

	"github.com/shreyashsri79/openair/internal/identity"
)

// Announce is what one device says about itself.
//
// It is the single record shape behind both discovery transports: the mDNS TXT
// record of §15.1 and the unicast fallback datagram of §15.2 carry exactly
// these four keys. Keeping one struct for both is what stops the fallback from
// drifting into a second, subtly different protocol.
//
// Nothing in here is authenticated. See PeerCandidate.
type Announce struct {
	DeviceID     identity.DeviceID // TXT "id"
	ProtoVersion uint8             // TXT "v"
	Port         int               // TXT "port"
	DisplayName  string            // TXT "n"
}

// maxDisplayName caps TXT `n`. A single mDNS TXT string is limited to 255
// bytes by the DNS wire format, and a display name is the only field a user
// controls; truncating here means a long name degrades the label rather than
// corrupting the record.
const maxDisplayName = 63

var (
	// ErrMalformedAnnounce reports a record that could not be parsed, or that
	// parsed but described nothing dialable. Announces arrive unsolicited from
	// anyone on the network, so this is an ordinary event, not a fault.
	ErrMalformedAnnounce = errors.New("discovery: malformed announce")

	// ErrWrongMagic reports a datagram on the fallback port that is not an
	// OpenAir announce at all -- another protocol sharing the port, or noise.
	ErrWrongMagic = errors.New("discovery: not an OpenAir discovery datagram")
)

// Validate reports whether the announce describes something worth emitting as
// a candidate. A syntactically invalid DeviceID or an unusable port is
// rejected here rather than at dial time, so garbage never reaches the
// connection manager.
func (a Announce) Validate() error {
	if !a.DeviceID.Valid() {
		return fmt.Errorf("%w: device id %q", ErrMalformedAnnounce, a.DeviceID)
	}
	if a.Port <= 0 || a.Port > 65535 {
		return fmt.Errorf("%w: port %d", ErrMalformedAnnounce, a.Port)
	}
	if a.ProtoVersion == 0 {
		return fmt.Errorf("%w: protocol version 0", ErrMalformedAnnounce)
	}
	return nil
}

// TXT renders the announce as DNS-SD TXT strings in the order §15.1 lists
// them. Order is not significant to a parser but a stable one keeps the
// records byte-identical between announcements, which keeps mDNS caches from
// treating a re-announce as a change.
func (a Announce) TXT() []string {
	return []string{
		"id=" + string(a.DeviceID),
		"v=" + strconv.Itoa(int(a.ProtoVersion)),
		"port=" + strconv.Itoa(a.Port),
		"n=" + truncateUTF8(a.DisplayName, maxDisplayName),
	}
}

// ParseTXT reads an announce out of DNS-SD TXT strings. Unknown keys are
// ignored: PROTOCOL.md §15.1 fixes four keys today and a later version adding
// a fifth must not make this build blind to the device.
func ParseTXT(txt []string) (Announce, error) {
	var a Announce
	for _, kv := range txt {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		switch k {
		case "id":
			a.DeviceID = identity.DeviceID(v)
		case "v":
			n, err := strconv.ParseUint(v, 10, 8)
			if err != nil {
				return Announce{}, fmt.Errorf("%w: version %q", ErrMalformedAnnounce, v)
			}
			a.ProtoVersion = uint8(n)
		case "port":
			n, err := strconv.Atoi(v)
			if err != nil {
				return Announce{}, fmt.Errorf("%w: port %q", ErrMalformedAnnounce, v)
			}
			a.Port = n
		case "n":
			a.DisplayName = truncateUTF8(v, maxDisplayName)
		}
	}
	if err := a.Validate(); err != nil {
		return Announce{}, err
	}
	return a, nil
}

// The unicast fallback datagram (PROTOCOL.md §15.2).
//
// PROTOCOL.md does not specify a byte layout for the fallback -- it says only
// that a peer MAY broadcast an announce -- so this is defined here and needs
// writing back into §15.2 before any other implementation exists.
//
//	 0       4     5     6
//	+-------+-----+-----+------------------------------+
//	| "OA2D"| ver | typ | n * (uint8 len, len bytes)   |
//	+-------+-----+-----+------------------------------+
//
// The body is the same key=value strings TXT() produces, length-prefixed
// rather than newline-separated because a display name may contain anything.
// A query carries an empty body; the responder answers with an announce sent
// unicast back to the source address, which is what makes discovery converge
// in one round trip instead of one beacon interval.
const (
	magic       = "OA2D"
	datagramVer = 1

	typeAnnounce = 1
	typeQuery    = 2

	// maxDatagram bounds a read buffer. The record is four short strings; a
	// kilobyte is roughly an order of magnitude of headroom and stays well
	// inside any path MTU, so the fallback never depends on IP fragmentation.
	maxDatagram = 1024
)

// EncodeAnnounce renders an announce as a fallback datagram.
func EncodeAnnounce(a Announce) []byte {
	return encodeDatagram(typeAnnounce, a.TXT())
}

// EncodeQuery renders a "who is out there" datagram. It carries only the
// asker's DeviceID, which exists so a responder can recognise its own
// broadcast coming back to it and not answer itself. It is not a claim of
// identity and nothing may be granted on the strength of it.
func EncodeQuery(from identity.DeviceID) []byte {
	return encodeDatagram(typeQuery, []string{"id=" + string(from)})
}

func encodeDatagram(typ byte, fields []string) []byte {
	buf := make([]byte, 0, 128)
	buf = append(buf, magic...)
	buf = append(buf, datagramVer, typ)
	for _, f := range fields {
		if len(f) > 255 {
			f = f[:255]
		}
		buf = append(buf, uint8(len(f)))
		buf = append(buf, f...)
	}
	return buf
}

// decodedDatagram is a parsed fallback datagram.
type decodedDatagram struct {
	isQuery bool
	// queryFrom is the DeviceID an incoming query claims to be from. Used
	// only to suppress answering our own broadcast.
	queryFrom identity.DeviceID
	announce  Announce
}

// decodeDatagram parses a fallback datagram. Every length is checked against
// what is actually present before it is used to slice, so a datagram claiming
// a 200-byte field in a 10-byte packet is rejected rather than panicking --
// this reads from an unauthenticated UDP socket that anyone on the network can
// write to.
func decodeDatagram(b []byte) (decodedDatagram, error) {
	if len(b) < 6 || string(b[:4]) != magic {
		return decodedDatagram{}, ErrWrongMagic
	}
	if b[4] != datagramVer {
		// Not an error worth surfacing: a future version legitimately shares
		// this port, and we simply cannot read it.
		return decodedDatagram{}, fmt.Errorf("%w: datagram version %d", ErrWrongMagic, b[4])
	}
	typ := b[5]
	rest := b[6:]

	var fields []string
	for len(rest) > 0 {
		n := int(rest[0])
		rest = rest[1:]
		if n > len(rest) {
			return decodedDatagram{}, fmt.Errorf("%w: field length %d exceeds %d remaining", ErrMalformedAnnounce, n, len(rest))
		}
		fields = append(fields, string(rest[:n]))
		rest = rest[n:]
	}

	switch typ {
	case typeQuery:
		d := decodedDatagram{isQuery: true}
		for _, f := range fields {
			if v, ok := strings.CutPrefix(f, "id="); ok {
				d.queryFrom = identity.DeviceID(v)
			}
		}
		return d, nil
	case typeAnnounce:
		a, err := ParseTXT(fields)
		if err != nil {
			return decodedDatagram{}, err
		}
		return decodedDatagram{announce: a}, nil
	default:
		return decodedDatagram{}, fmt.Errorf("%w: datagram type %d", ErrWrongMagic, typ)
	}
}

// truncateUTF8 cuts s to at most n bytes without splitting a rune. Cutting
// mid-rune would put invalid UTF-8 in a TXT record, which §15.1 requires be
// UTF-8.
func truncateUTF8(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && s[n]&0xC0 == 0x80 {
		n--
	}
	return s[:n]
}

// rankAddrs orders candidate addresses best-first and drops the ones that
// cannot be dialed.
//
// v1.0 (openair-gui/internal/sender/discover.go) picked exactly one address
// and hard-excluded 192.168.56.0/24 because VirtualBox's host-only adapter
// answers there and never routes. v2 emits every address and only *orders*
// them: the connection manager races candidates (HLD §3.3), so a dud address
// costs one failed dial, whereas dropping the only address a peer has costs
// the transfer. The VirtualBox range is therefore demoted, not deleted.
func rankAddrs(ips []net.IP, port int) []string {
	type scored struct {
		addr  string
		score int
	}
	var out []scored
	seen := map[string]bool{}

	for _, ip := range ips {
		if ip == nil || ip.IsUnspecified() || ip.IsMulticast() {
			continue
		}
		s := ip.String()
		if seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, scored{addr: net.JoinHostPort(s, strconv.Itoa(port)), score: addrScore(ip)})
	}

	// Insertion sort: an address list is single digits long, and a stable
	// order matters more than the asymptotics.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].score < out[j-1].score; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}

	addrs := make([]string, len(out))
	for i, s := range out {
		addrs[i] = s.addr
	}
	return addrs
}

// addrScore is lower-is-better. The ordering encodes what is most likely to be
// the peer's real LAN path.
func addrScore(ip net.IP) int {
	v4 := ip.To4()
	switch {
	case ip.IsLoopback():
		// Only ever right when both ends are on this machine, which happens in
		// tests and effectively nowhere else.
		return 60
	case v4 != nil && v4[0] == 192 && v4[1] == 168 && v4[2] == 56:
		// VirtualBox host-only. Answers, never routes anywhere useful.
		return 50
	case v4 != nil && isPrivateV4(v4):
		return 10
	case v4 != nil && ip.IsLinkLocalUnicast():
		return 40
	case v4 != nil:
		// A public v4 on the LAN path: unusual but perfectly dialable.
		return 20
	case ip.IsLinkLocalUnicast():
		// IPv6 link-local needs a zone to dial and we do not always have one.
		return 45
	default:
		// Global IPv6, including ULA.
		return 15
	}
}

func isPrivateV4(v4 net.IP) bool {
	switch {
	case v4[0] == 10:
		return true
	case v4[0] == 172 && v4[1]&0xF0 == 16:
		return true
	case v4[0] == 192 && v4[1] == 168:
		return true
	}
	return false
}

// hostPortsFromAddr builds the dial addresses for a peer heard over unicast:
// the source IP of the datagram paired with the QUIC port it announced. The
// announced port is used, never the datagram's source port -- the beacon and
// the QUIC listener are different sockets.
func hostPortsFromAddr(src *net.UDPAddr, port int) []string {
	if src == nil || src.IP == nil {
		return nil
	}
	return rankAddrs([]net.IP{src.IP}, port)
}

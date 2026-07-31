package path

import (
	"errors"
	"fmt"

	"github.com/shreyashsri79/openair/internal/identity"
)

// Punch probes, §18 step 3.
//
// A probe is the packet both sides spray at each other's candidate addresses
// to open a NAT mapping and to find out which candidate pair actually works.
// PROTOCOL.md specifies the signalling (PunchRequest/PunchReady) but not the
// probe itself, which it has to leave open — the probe is a property of the
// implementation, not of the interoperable protocol, and two OpenAir peers are
// the only parties that ever exchange one.
//
// Format:
//
//	0        4      5                    21                   37
//	+--------+------+--------------------+--------------------+
//	| magic  | kind | punch token (16)   | sender id (16)     |
//	+--------+------+--------------------+--------------------+
//
// The magic starts with a zero byte, which clears QUIC's fixed bit, so a probe
// can never be mistaken for a QUIC packet and no QUIC packet can be mistaken
// for a probe (isQUIC).
//
// The token is the security of the exchange. It is sixteen random bytes carried
// to the peer inside the already-encrypted session, so only that peer can put
// it in a probe. An address is not accepted because a probe arrived from it —
// that would let anyone who can guess a token redirect our packets — but only
// once a probe *we* sent to that address is answered, which needs two-way
// reachability with something holding the token.
const (
	probeMagic0 = 0x00
	probeMagic1 = 'O'
	probeMagic2 = 'A'
	probeMagic3 = 'P'

	// TokenLen is §18's punch_token: 16 random bytes, echoed to correlate
	// attempts.
	TokenLen = 16

	probeSize = 4 + 1 + TokenLen + identity.DeviceIDLen
)

// probeKind distinguishes the two directions of one connectivity check.
type probeKind byte

const (
	probePing probeKind = 1
	probePong probeKind = 2
)

// errNotProbe reports a packet that is not a probe. It is expected rather than
// exceptional: every STUN response and every stray datagram takes this path.
var errNotProbe = errors.New("path: not a punch probe")

// encodeProbe builds one probe packet.
func encodeProbe(kind probeKind, token []byte, sender identity.DeviceID) ([]byte, error) {
	if len(token) != TokenLen {
		return nil, fmt.Errorf("path: punch token is %d bytes, want %d", len(token), TokenLen)
	}
	if len(sender) != identity.DeviceIDLen {
		return nil, fmt.Errorf("path: sender %q is not a device id", sender)
	}
	b := make([]byte, 0, probeSize)
	b = append(b, probeMagic0, probeMagic1, probeMagic2, probeMagic3, byte(kind))
	b = append(b, token...)
	b = append(b, sender...)
	return b, nil
}

// decodeProbe reads a probe, or reports errNotProbe.
func decodeProbe(b []byte) (probeKind, []byte, identity.DeviceID, error) {
	if len(b) != probeSize ||
		b[0] != probeMagic0 || b[1] != probeMagic1 || b[2] != probeMagic2 || b[3] != probeMagic3 {
		return 0, nil, "", errNotProbe
	}
	kind := probeKind(b[4])
	if kind != probePing && kind != probePong {
		return 0, nil, "", errNotProbe
	}
	token := b[5 : 5+TokenLen]
	sender := identity.DeviceID(b[5+TokenLen:])
	if !sender.Valid() {
		return 0, nil, "", errNotProbe
	}
	return kind, token, sender, nil
}

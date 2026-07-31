// Package relay is PROTOCOL.md §17: a forwarder for paths where direct
// connectivity fails.
//
// It moves ciphertext between two peers and holds no keys. The payload of every
// data frame is a complete QUIC packet, so end-to-end encryption is exactly the
// same whether a path is relayed or direct — the relay is a network element,
// not a participant (PRD R27). What its operator learns is which DeviceIDs talk
// to each other, when, and how much; not content, and the same self-hosting
// argument applies as for §16.
package relay

import (
	"encoding/binary"
	"fmt"
	"io"

	"github.com/shreyashsri79/openair/internal/identity"
)

// Data frame, §17:
//
//	0                        16                     20
//	+------------------------+----------------------+
//	| device_id (16)         | length (u32)         |
//	+------------------------+----------------------+
//	| payload — an opaque QUIC datagram             |
//	+-----------------------------------------------+
//
// The 16-byte field is the **destination** on the way in and the **source** on
// the way out (D-64). §17 names it `dst_device_id` in both directions, which
// cannot be right: a client only ever receives frames addressed to itself, so a
// destination field on the inbound path would carry no information, and the
// receiver would have no way to tell which peer a packet came from. With one
// QUIC connection per peer riding the relay, that attribution is not optional.
const (
	frameHeaderSize = identity.DeviceIDLen + 4

	// MaxPayload bounds one relayed packet. A QUIC datagram is at most one
	// path MTU, so this is generous by an order of magnitude and exists to
	// bound a hostile client rather than to be reached.
	MaxPayload = 16 << 10
)

// writeFrame writes one data frame.
func writeFrame(w io.Writer, peer identity.DeviceID, payload []byte) error {
	if len(peer) != identity.DeviceIDLen {
		return fmt.Errorf("relay: device id %q is %d bytes, want %d", peer, len(peer), identity.DeviceIDLen)
	}
	if len(payload) > MaxPayload {
		return fmt.Errorf("relay: payload is %d bytes, over the %d cap", len(payload), MaxPayload)
	}
	buf := make([]byte, 0, frameHeaderSize+len(payload))
	buf = append(buf, peer...)
	buf = binary.LittleEndian.AppendUint32(buf, uint32(len(payload)))
	buf = append(buf, payload...)
	_, err := w.Write(buf)
	return err
}

// readFrame reads one data frame. The length is checked before allocating.
func readFrame(r io.Reader) (identity.DeviceID, []byte, error) {
	var hdr [frameHeaderSize]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return "", nil, err
	}
	peer := identity.DeviceID(hdr[:identity.DeviceIDLen])
	length := binary.LittleEndian.Uint32(hdr[identity.DeviceIDLen:])
	if length > MaxPayload {
		return "", nil, fmt.Errorf("relay: frame claims %d bytes, over the %d cap", length, MaxPayload)
	}
	payload := make([]byte, length)
	if _, err := io.ReadFull(r, payload); err != nil {
		return "", nil, err
	}
	if !peer.Valid() {
		return "", nil, fmt.Errorf("relay: frame names %q, which is not a device id", peer)
	}
	return peer, payload, nil
}

// Addr is a peer reached through a relay, as a net.Addr so that a QUIC stack
// can carry it around without knowing what it is.
//
// This is the whole trick that lets a relayed path be a real QUIC connection
// rather than a tunnel with its own semantics: quic-go takes a net.PacketConn
// and addresses it hands back, and never requires those addresses to be UDP.
type Addr struct{ DeviceID identity.DeviceID }

func (a Addr) Network() string { return "openair-relay" }
func (a Addr) String() string  { return string(a.DeviceID) }

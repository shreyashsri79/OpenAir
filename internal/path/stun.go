package path

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"net"
	"net/netip"
)

// STUN, the reflexive half of §18's candidate gathering.
//
// A device behind NAT cannot know the address the world sees it at, and two
// devices that both have only their private addresses have nothing to punch
// towards. A STUN Binding request answers exactly that one question and
// nothing else, which is why §18 names it rather than something larger: the
// server tells the client the source address it observed, and the client puts
// that in its candidate list.
//
// This is RFC 8489's Binding method only — no authentication, no attributes
// beyond XOR-MAPPED-ADDRESS, no TURN. It has to be spoken on the same socket
// the QUIC traffic uses, or the mapping it reports belongs to a different port
// than the one being punched, which is the usual way a hand-rolled
// implementation of this ends up reporting a plausible and useless address.
const (
	stunHeaderSize = 20
	stunCookie     = 0x2112A442

	stunBindingRequest  = 0x0001
	stunBindingResponse = 0x0101

	attrMappedAddress    = 0x0001
	attrXorMappedAddress = 0x0020

	familyIPv4 = 0x01
	familyIPv6 = 0x02
)

// txID is a STUN transaction ID: 96 bits correlating a response to a request.
type txID [12]byte

// newTxID draws a fresh transaction ID.
func newTxID() (txID, error) {
	var id txID
	_, err := rand.Read(id[:])
	return id, err
}

// isSTUN reports whether a packet is a STUN message. The two leading zero bits
// and the magic cookie are what RFC 8489 §6 offers for demultiplexing STUN
// from anything else sharing a port, and they are enough here: a QUIC packet
// always has the fixed bit set, so the first test alone already separates the
// two.
func isSTUN(b []byte) bool {
	return len(b) >= stunHeaderSize && b[0]&0xC0 == 0 && beUint32(b[4:8]) == stunCookie
}

// stunMessage builds a header-plus-attributes message.
func stunMessage(typ uint16, id txID, attrs []byte) []byte {
	b := make([]byte, stunHeaderSize+len(attrs))
	binary.BigEndian.PutUint16(b[0:2], typ)
	binary.BigEndian.PutUint16(b[2:4], uint16(len(attrs)))
	binary.BigEndian.PutUint32(b[4:8], stunCookie)
	copy(b[8:20], id[:])
	copy(b[20:], attrs)
	return b
}

// bindingRequest is what a client sends.
func bindingRequest(id txID) []byte { return stunMessage(stunBindingRequest, id, nil) }

// bindingResponse answers a request, telling the client where its packet came
// from.
func bindingResponse(id txID, from netip.AddrPort) []byte {
	return stunMessage(stunBindingResponse, id, xorMappedAddress(id, from))
}

// xorMappedAddress encodes XOR-MAPPED-ADDRESS (RFC 8489 §14.2). The address is
// obfuscated with the cookie so that middleboxes rewriting anything that looks
// like an address in a payload do not silently corrupt it — which is the whole
// reason the XOR form replaced the plain one.
func xorMappedAddress(id txID, ap netip.AddrPort) []byte {
	ip := ap.Addr()
	port := ap.Port() ^ uint16(stunCookie>>16)

	var value []byte
	if ip.Is4() {
		v4 := ip.As4()
		value = make([]byte, 8)
		value[1] = familyIPv4
		binary.BigEndian.PutUint16(value[2:4], port)
		binary.BigEndian.PutUint32(value[4:8], binary.BigEndian.Uint32(v4[:])^stunCookie)
	} else {
		v6 := ip.As16()
		value = make([]byte, 20)
		value[1] = familyIPv6
		binary.BigEndian.PutUint16(value[2:4], port)
		var mask [16]byte
		binary.BigEndian.PutUint32(mask[0:4], stunCookie)
		copy(mask[4:], id[:])
		for i := range v6 {
			value[4+i] = v6[i] ^ mask[i]
		}
	}

	attr := make([]byte, 4+len(value))
	binary.BigEndian.PutUint16(attr[0:2], attrXorMappedAddress)
	binary.BigEndian.PutUint16(attr[2:4], uint16(len(value)))
	copy(attr[4:], value)
	return attr
}

// parseSTUN reads a STUN message, returning its type, transaction ID and the
// mapped address it carries, if any.
func parseSTUN(b []byte) (typ uint16, id txID, mapped netip.AddrPort, err error) {
	if !isSTUN(b) {
		return 0, id, netip.AddrPort{}, fmt.Errorf("path: not a STUN message")
	}
	typ = binary.BigEndian.Uint16(b[0:2])
	length := int(binary.BigEndian.Uint16(b[2:4]))
	copy(id[:], b[8:20])
	if stunHeaderSize+length > len(b) {
		return 0, id, netip.AddrPort{}, fmt.Errorf("path: STUN message claims %d bytes of attributes, has %d",
			length, len(b)-stunHeaderSize)
	}

	attrs := b[stunHeaderSize : stunHeaderSize+length]
	for len(attrs) >= 4 {
		attrType := binary.BigEndian.Uint16(attrs[0:2])
		attrLen := int(binary.BigEndian.Uint16(attrs[2:4]))
		if 4+attrLen > len(attrs) {
			break
		}
		value := attrs[4 : 4+attrLen]
		switch attrType {
		case attrXorMappedAddress:
			if ap, ok := decodeAddress(id, value, true); ok {
				mapped = ap
			}
		case attrMappedAddress:
			// Accepted for servers old enough to send only this one. The XOR
			// form wins if both are present, because the loop takes the later
			// assignment only when it decodes.
			if ap, ok := decodeAddress(id, value, false); ok && !mapped.IsValid() {
				mapped = ap
			}
		}
		// Attributes are padded to a four-byte boundary; the padding is not
		// counted in the length, and a parser that forgets it walks off into
		// the middle of the next attribute.
		advance := 4 + attrLen
		if pad := attrLen % 4; pad != 0 {
			advance += 4 - pad
		}
		if advance > len(attrs) {
			break
		}
		attrs = attrs[advance:]
	}
	return typ, id, mapped, nil
}

// decodeAddress reads a MAPPED-ADDRESS or XOR-MAPPED-ADDRESS value.
func decodeAddress(id txID, v []byte, xor bool) (netip.AddrPort, bool) {
	if len(v) < 4 {
		return netip.AddrPort{}, false
	}
	port := binary.BigEndian.Uint16(v[2:4])
	if xor {
		port ^= uint16(stunCookie >> 16)
	}
	switch v[1] {
	case familyIPv4:
		if len(v) < 8 {
			return netip.AddrPort{}, false
		}
		raw := binary.BigEndian.Uint32(v[4:8])
		if xor {
			raw ^= stunCookie
		}
		var a [4]byte
		binary.BigEndian.PutUint32(a[:], raw)
		return netip.AddrPortFrom(netip.AddrFrom4(a), port), true
	case familyIPv6:
		if len(v) < 20 {
			return netip.AddrPort{}, false
		}
		var a [16]byte
		copy(a[:], v[4:20])
		if xor {
			var mask [16]byte
			binary.BigEndian.PutUint32(mask[0:4], stunCookie)
			copy(mask[4:], id[:])
			for i := range a {
				a[i] ^= mask[i]
			}
		}
		return netip.AddrPortFrom(netip.AddrFrom16(a), port), true
	}
	return netip.AddrPort{}, false
}

// ServeSTUN answers Binding requests on pc until ctx is cancelled.
//
// It exists so that self-hosting OpenAir means running the servers in this
// repo and nothing else: §18 needs a reflexive address, and a user who has
// already stood up a rendezvous server should not also have to find a STUN
// server to trust (D-68). It is a plain RFC 8489 responder, so pointing a
// client at somebody else's works exactly as well.
func ServeSTUN(ctx context.Context, pc net.PacketConn) error {
	go func() {
		<-ctx.Done()
		_ = pc.SetReadDeadline(timeInPast())
	}()

	buf := make([]byte, 1500)
	for {
		n, from, err := pc.ReadFrom(buf)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		if !isSTUN(buf[:n]) {
			continue
		}
		typ, id, _, err := parseSTUN(buf[:n])
		if err != nil || typ != stunBindingRequest {
			continue
		}
		ap, ok := addrPortOf(from)
		if !ok {
			continue
		}
		if _, err := pc.WriteTo(bindingResponse(id, ap), from); err != nil && ctx.Err() != nil {
			return nil
		}
	}
}

// addrPortOf converts a net.Addr to a netip.AddrPort, unmapping the
// IPv4-in-IPv6 form a dual-stack socket reports so that the address handed back
// to a client is the one it will recognise as its own.
func addrPortOf(a net.Addr) (netip.AddrPort, bool) {
	switch v := a.(type) {
	case *net.UDPAddr:
		ap, ok := netip.AddrFromSlice(v.IP)
		if !ok {
			return netip.AddrPort{}, false
		}
		return netip.AddrPortFrom(ap.Unmap(), uint16(v.Port)), true
	}
	ap, err := netip.ParseAddrPort(a.String())
	if err != nil {
		return netip.AddrPort{}, false
	}
	return netip.AddrPortFrom(ap.Addr().Unmap(), ap.Port()), true
}

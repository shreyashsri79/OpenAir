// Package path is PROTOCOL.md §18: one connection per peer, over whichever
// path is available, upgraded live.
//
// # Why migration happens here and not in QUIC
//
// quic-go can migrate a connection — `Conn.AddPath` probes a new path and
// switches to it — but only from the client side, and only onto a different
// *local* socket: the remote address is carried over unchanged
// (`switchToNewPath` keeps `c.conn.RemoteAddr()`). Neither fits §18. The
// upgrade this milestone needs is relay-to-direct, where the remote address is
// the whole difference, and it has to work on the accepting side too, because
// the device behind the NAT is as often the one accepting.
//
// So the path lives underneath QUIC instead. A Conn is one UDP socket plus an
// optional relay connection, and it presents each peer to the QUIC stack above
// as a single stable address — Addr{DeviceID} — no matter which of the two is
// currently carrying its packets. Switching a peer from the relay to a punched
// direct path is then a map write here: the connection ID does not change, no
// path validation is triggered, no stream is reset, and a transfer in flight
// does not restart (PRD R9). It is the arrangement Tailscale's magicsock uses
// for the same reason.
//
// What it costs is that QUIC does not know the path changed. Its congestion
// controller keeps an RTT estimate measured on the old path and re-converges
// (BBR does this within a few round trips, D-14), and the effective MTU may
// differ between the two. Both are recoverable; a restarted transfer is not.
//
// # What shares the socket, and why it must
//
// Hole punching only works if the packets that open the NAT mapping leave from
// the same source port the QUIC traffic will use, so this one socket carries
// four things: QUIC to and from ordinary LAN peers, QUIC to and from punched
// peers, STUN requests that discover the reflexive address, and the punch
// probes themselves. Read demultiplexes them; only QUIC packets are returned to
// the caller.
package path

import (
	"encoding/binary"
	"net/netip"

	"github.com/shreyashsri79/openair/internal/identity"
)

// Addr is a peer addressed by DeviceID rather than by ip:port.
//
// This is the address the QUIC stack sees for any peer reached over the relay
// or over a punched path. It stays the same across a migration, which is what
// makes the migration invisible: to quic-go nothing about the connection's
// remote address has changed.
type Addr struct{ DeviceID identity.DeviceID }

func (a Addr) Network() string { return "openair-path" }
func (a Addr) String() string  { return string(a.DeviceID) }

// Path classes, §7.2's PathClass as the strings PathInfo carries.
const (
	ClassLAN     = "lan"
	ClassPunched = "punched"
	ClassRelayed = "relayed"
)

// classOf names a validated direct address. A private or loopback address was
// reached without traversing anything, which is what §7.2 means by "lan"; a
// public one was reached because a punch opened a mapping.
func classOf(ap netip.AddrPort) string {
	ip := ap.Addr()
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() {
		return ClassLAN
	}
	return ClassPunched
}

// isQUIC reports whether a received packet can be a QUIC packet at all.
//
// RFC 9000 §17.2/§17.3 require the second-most-significant bit of the first
// byte — the fixed bit — to be set on every QUIC packet. Probes here clear it,
// which is what keeps the demultiplexing at the top of the read loop free of
// guesswork rather than depending on a magic value not colliding.
func isQUIC(b []byte) bool { return len(b) > 0 && b[0]&0x40 != 0 }

// beUint32 reads a big-endian u32, which is the byte order STUN uses. The
// rest of this repo is little-endian because PROTOCOL.md says so; STUN is not
// ours to choose.
func beUint32(b []byte) uint32 { return binary.BigEndian.Uint32(b) }

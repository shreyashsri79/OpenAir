// Package discovery finds paired peers and emits candidates. It never dials --
// dialling belongs to internal/conn (HLD 3.2).
//
// LAN: mDNS over _openair._udp, TXT carrying device id, protocol version, port
// and display name. Note the change from v1.0's _openair._tcp; v2 is QUIC.
// A unicast broadcast fallback covers multicast-hostile networks.
//
// Discovery is a hint, not an authority. A discovered peer is still
// authenticated by pinned key, so a hostile announce achieves nothing beyond a
// failed handshake.
//
// WAN discovery via the rendezvous client is Phase 2 (PROTOCOL.md 16).
package discovery

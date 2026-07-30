// Package conn establishes and maintains one QUIC connection per peer (D-6).
//
// Phase 1 is a plain LAN dial. Phase 2 adds the path racing described in
// PROTOCOL.md 18: start on the relay immediately so the session is usable in
// about one round trip, race direct candidates and coordinated hole punching in
// parallel, then migrate to a better path without breaking streams (PRD R9).
//
// Capabilities are never told which path they are on -- only a PathInfo hint --
// so capability parity across paths stays structural rather than a per-feature
// obligation (PRD R8).
//
// Transport is a vendored quic-go carrying BBRv1 (D-14, D-16) and, from Phase 2,
// the Windows send and receive offload work (D-22, D-23, D-32).
package conn

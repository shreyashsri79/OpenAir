// Package session is the layer every capability sits on: envelope framing,
// capability negotiation, authorisation, and flow arbitration.
//
// Framing is an 8-byte fixed header -- version, capability id, message type,
// length -- wrapping a protobuf payload, so demultiplexing costs no allocation.
// Bulk chunk frames and input datagrams bypass protobuf entirely.
// Wire values are offset by one from the generated enum values; convert, never
// cast (D-34).
//
// Negotiation takes the lower of both peers' protocol versions and the
// intersection of their capabilities. Unknown capability ids and unknown message
// types are ignored rather than fatal, which is what makes mixed-version fleets
// work (PRD R32).
//
// Authorisation middleware runs before any message reaches a capability: Owned
// requests must carry an AuthProof signed by the peer's pinned privilege key and
// bound to this device, this capability and this message type. Trusted-level
// operations use the hybrid consent model in D-25.
//
// Flow arbitration is here rather than in the transport because quic-go exposes
// no stream prioritisation at all (D-24). Bulk is throttled to a floor while a
// high-bandwidth capability runs.
package session

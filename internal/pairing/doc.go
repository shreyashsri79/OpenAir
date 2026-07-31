// Package pairing implements PROTOCOL.md §5 (pairing), §6.1 (revocation) and
// §6.4 (capability grants) on top of an established session.
//
// The shape of the thing, because it is not obvious from the file list:
//
//   - The out-of-band offer (§5.1) is a PairOffer encoded as one printable
//     string, in offer.go. A QR code carries that string; manual entry types
//     it. Rendering the QR is a UI concern and lives outside this package.
//   - The exchange (§5.2) runs over the control stream as capID 0 messages, so
//     Handler is a session.Handler registered at capID 0. Handler.Initiate
//     drives the side that scanned the offer and dialled; Handler.Await drives
//     the side that displayed it and accepted.
//   - The six-digit short authentication string is computed in sas.go from a
//     transcript both sides derive independently. It is the entire security of
//     pairing: TLS is unauthenticated at this point, so a man in the middle is
//     detected only by the two SAS values differing. There is deliberately no
//     way to skip the comparison, and Config.Confirm may not be nil.
//   - Authorize is the trust-store gate that replaces M1's nil
//     session.Config.Authorize. Unpaired peers are refused unless a pairing
//     window is explicitly open.
//   - Guard is the mid-session enforcement point required by §6.1: a revoke
//     changes what a live session may do without waiting for it to end.
package pairing

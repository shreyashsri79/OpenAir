// Package mobile is the gomobile-bindable façade over the OpenAir v2 core.
//
// It exists because `gomobile bind` cannot bind `internal/...`: the binding has
// to be an importable package outside the internal tree, so this package wraps
// internal/{identity,conn,session,caps/files} and exposes the same operations
// the `openair` CLI performs (D-10, D-31 — one Go core, bound with gomobile,
// shared by every platform).
//
// # What the API may contain
//
// gobind only understands a subset of Go. Everything exported from this package
// must stay inside it, or `gomobile bind` fails with an unhelpful message:
//
//   - Parameters and results: bool, string, []byte, the signed integer types,
//     float32/float64, pointers to exported structs declared here, and
//     interfaces declared here. Nothing else.
//   - No unsigned integers. The core counts bytes in uint64; this package
//     converts to int64 at the boundary. Sizes big enough to overflow int64 do
//     not exist.
//   - No slices except []byte, so no []string and no []Struct. Lists are
//     modelled as an object with an index accessor — see FileList and Offer.
//   - No maps, no channels, no variadics, no generics, no functions as values.
//     Callbacks are Java-implemented interfaces (ProgressCallback and friends).
//   - Multiple returns only in the form (T, error), and error must be last.
//     A Go error surfaces in Kotlin as a thrown Exception.
//
// # Threading
//
// Sender.SendFiles blocks for the whole transfer and must be called off the
// Android main thread. Receiver.Start returns immediately and runs its accept
// loop on a Go goroutine.
//
// Every callback is invoked from a Go goroutine, never on the Android main
// looper. The two verifier callbacks are synchronous decisions: the transfer is
// held open until they return, so a Kotlin implementation that has to ask the
// user must block that thread until the user answers.
//
// # Milestone scope
//
// M1 through M3: pair with a device once (Pairing), find devices on the local
// network (Discovery), and transfer files to a paired one (Sender, Receiver).
// There is no unlock flow (M6), so nothing here reaches Owned-level operations.
//
// Pairing is the security boundary, and it is not optional. A device that was
// never paired is refused by the trust store on both ends, before any
// capability message is dispatched, and no callback can override that. The
// verifier hooks that remain are product choices on top of it: OfferVerifier
// still defaults to refusing, because an unattended phone must not silently
// accept files it was never told about, while PeerVerifier is now an optional
// second prompt rather than the thing standing between a stranger and the disk.
//
// SASVerifier is the exception and must always be implemented: the six digits
// are the whole of pairing's security, and §5.2 forbids a way to skip them.
package mobile

// Version identifies the binding's feature level. It is not the protocol
// version (PROTOCOL.md §3 owns that) — it exists so a shell can tell which
// operations the .aar it was built against actually has.
const Version = "m3"

// PlatformName is what this device reports in Hello. PROTOCOL.md §4 enumerates
// exactly four values and this binding only ever runs on one of them.
const PlatformName = "android"

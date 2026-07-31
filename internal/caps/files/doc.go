// Package files transfers files over N parallel QUIC streams, the direct
// descendant of v1.0's engine.
//
// Chunk frames keep v1.0's 12-byte header byte-for-byte: little-endian uint64
// offset, uint32 size, then data. The receiver writes each at its offset, so
// chunks may arrive in any order. Per-chunk SHA-256 digests live in a manifest
// rather than the frame, keeping the hot path at 12 bytes.
//
// Stream count should default to one or two. Measurement says more never helps
// QUIC and costs 2.2x on Windows at four streams (D-13, D-33) -- v1.0's eight
// workers are actively harmful here. Treat higher values as a tuning escape
// hatch, not the norm.
//
// Sharp edge: reject any path that is absolute, contains "..", or resolves
// outside the destination root. Path traversal is the obvious attack on a file
// transfer protocol and the check belongs here, not in the UI. path.go owns
// that check and refuses backslashes and volume separators too, so one offer
// cannot mean two things on Linux and Windows.
//
// The offset in a chunk frame is a transfer-global offset into the offered
// files concatenated in offer order, because the 12-byte frame carries no file
// identifier and nothing else would tell the receiver which file a chunk
// belongs to. Chunks therefore never span a file boundary: the last chunk of
// each file is short.
//
// Data lands in "name.oapart" and is renamed to "name" only when every chunk
// has verified against the manifest, so a cancelled or interrupted transfer
// never presents a partial file as complete. The verified-chunk bitmap is
// persisted alongside, which is what makes resume survive a reconnect rather
// than only a retry inside one process.
package files

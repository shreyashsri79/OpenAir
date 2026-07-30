// Package clipboard pushes clipboard content between peers.
//
// Manual push is the guaranteed path on every platform. Automatic sync is
// opt-in and best-effort where OS policy restricts background clipboard reads
// (PRD R19, K3). Content is never persisted by relays or logged in plaintext
// (PRD R20).
//
// Runs on the identity key, not the privilege key, so it keeps working while an
// unlock session is expired (D-20). Gating it would have made the auth policy
// visible in the place users would least tolerate it.
package clipboard

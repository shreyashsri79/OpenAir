package mobile

// The callback interfaces. Each becomes a Java interface the shell implements;
// gomobile wraps the implementation so Go can call back into it.
//
// All of them are invoked from Go goroutines. On Android that means none of
// them run on the main looper, so an implementation that touches UI state must
// post to it.

// ProgressCallback receives transfer progress from either direction.
//
// Progress originates on the receiving side and is relayed to the sender
// (PROTOCOL.md §8.5), so it arrives at roughly 1 Hz rather than per chunk.
// totalBytes is -1 when the total is not yet known, which happens on the
// sending side for progress that arrives before the plan is registered; a shell
// must render that as indeterminate rather than dividing by it.
type ProgressCallback interface {
	OnProgress(transferID string, bytesDone int64, totalBytes int64)
}

// PeerVerifier is the human check on a peer's identity.
//
// It is called once per session, after the handshake has revealed who the peer
// is and before any file data moves — on the sending side after dialling, on
// the receiving side before the session is admitted. Returning false aborts.
//
// Until pairing exists (M2), nothing is pinned in advance and this is the only
// thing standing between the user and an arbitrary peer. The implementation
// must actually show peer.Fingerprint() and wait for an answer; returning true
// unconditionally reduces the milestone's security to nothing.
//
// The call blocks the session, so a Kotlin implementation raising a dialog must
// block its (background) thread until the user decides.
type PeerVerifier interface {
	VerifyPeer(peer *PeerInfo) bool
}

// OfferVerifier is the human check on what is about to be written.
//
// Called on the receiving side after PeerVerifier has admitted the peer, once
// per inbound transfer. Returning false rejects the offer and writes nothing.
type OfferVerifier interface {
	VerifyOffer(peer *PeerInfo, offer *Offer) bool
}

// TransferCallback reports the end of an inbound transfer.
//
// The sending side learns the outcome from SendFiles returning, but a receiver
// has no such return: inbound transfers arrive on the session's control loop.
// ok is false when the transfer failed, including when every byte arrived and
// then failed its digest check — which is why "progress reached the total" is
// not a substitute for this signal.
type TransferCallback interface {
	OnComplete(transferID string, ok bool)
}

// ErrorCallback reports a failure that happens outside any call the shell made,
// which in practice means the receiver's accept loop dying.
//
// stage names where it happened ("accept", "listen") so a shell can decide
// whether to surface it or just stop the receiver.
type ErrorCallback interface {
	OnError(stage string, message string)
}

// SessionCallback reports session lifecycle on the receiving side, so a shell
// can show "connected to X" between admitting a peer and the offer arriving.
type SessionCallback interface {
	OnPeerConnected(peer *PeerInfo)
}

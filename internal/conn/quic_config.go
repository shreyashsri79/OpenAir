package conn

import (
	"time"

	"github.com/apernet/quic-go"
)

// quicConfig returns the quic.Config shared by dial and accept. The window
// and timing values are ported from oabench/bench/quic.go, where they were
// measured against real hardware rather than guessed (BUILD-PLAN.md M1d).
//
// EnableDatagrams is set even though Phase 1 sends none: PROTOCOL.md §1
// requires datagram support at the transport so that capabilities added
// later need no version bump.
func quicConfig() *quic.Config {
	return &quic.Config{
		MaxIdleTimeout:                 60 * time.Second,
		KeepAlivePeriod:                5 * time.Second,
		InitialStreamReceiveWindow:     16 << 20,
		MaxStreamReceiveWindow:         16 << 20,
		InitialConnectionReceiveWindow: 64 << 20,
		MaxConnectionReceiveWindow:     64 << 20,
		MaxIncomingStreams:             1024,
		EnableDatagrams:                true,
	}
}

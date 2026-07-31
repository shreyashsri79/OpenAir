package main

import (
	"bytes"
	"sync"
)

// lockedBuffer is a bytes.Buffer safe for the receiver's goroutines to write
// to while the test goroutine reads it in a failure message. Without it the
// race detector fires on the diagnostic path rather than on anything real,
// which is a good way to lose an afternoon.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

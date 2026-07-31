package clipboard

import (
	"context"
	"sync"
	"testing"
	"time"
)

// M13's whole risk is a loop, so these tests are mostly about what does *not*
// get sent.

// TestPeerContentIsNotSentBack: the echo, in its simplest form. A applies what
// B sent; A's next poll sees it; A must say nothing.
func TestPeerContentIsNotSentBack(t *testing.T) {
	s := NewSyncState()

	// Something arrives and is applied.
	if !s.ShouldApply("from the peer", time.Now()) {
		t.Fatal("new content from a peer was not applied")
	}
	s.Applied("from the peer")

	// The watcher's next poll sees exactly that.
	if s.ShouldSend("from the peer") {
		t.Fatal("the watcher sent back what the peer had just sent")
	}

	// A genuine local copy afterwards is still sent.
	if !s.ShouldSend("something the user copied") {
		t.Fatal("a local change was suppressed")
	}
}

// TestTheSameContentIsNotAppliedTwice, which is what a source re-posting after
// a reconnect looks like.
func TestTheSameContentIsNotAppliedTwice(t *testing.T) {
	s := NewSyncState()
	now := time.Now()

	if !s.ShouldApply("x", now) {
		t.Fatal("first apply refused")
	}
	s.Applied("x")
	if s.ShouldApply("x", now.Add(time.Second)) {
		t.Fatal("the same content was applied twice")
	}
}

// TestSimultaneousEditsConverge is the case the build plan names: both devices
// copy something at the same moment and push to each other. Without a rule they
// swap clipboards and keep pushing; with origin_ts, the later copy wins on both
// and the exchange stops.
func TestSimultaneousEditsConverge(t *testing.T) {
	a, b := NewSyncState(), NewSyncState()

	// Both copy locally, a moment apart.
	tA := time.Now()
	if !a.ShouldSend("from A") {
		t.Fatal("A did not send its own copy")
	}
	time.Sleep(2 * time.Millisecond)
	tB := time.Now()
	if !b.ShouldSend("from B") {
		t.Fatal("B did not send its own copy")
	}

	// Each receives the other's push. A's local copy is older, so B's wins;
	// B's is newer, so A's loses.
	if !a.ShouldApply("from B", tB) {
		t.Fatal("A refused the newer copy from B")
	}
	a.Applied("from B")
	if b.ShouldApply("from A", tA) {
		t.Fatal("B applied a copy older than its own, which is how the ping-pong starts")
	}

	// And now the exchange stops. A's next poll sees B's content, which it must
	// not send back; B's poll sees its own, which it has already sent.
	if a.ShouldSend("from B") {
		t.Fatal("A pushed back the content it had just applied")
	}
	if b.ShouldSend("from B") {
		t.Fatal("B pushed content it had already sent")
	}
}

// TestEmptyContentIsIgnored: a cleared clipboard is not a copy, and pushing an
// empty string to every device is the least useful thing this could do.
func TestEmptyContentIsIgnored(t *testing.T) {
	s := NewSyncState()
	if s.ShouldSend("") {
		t.Fatal("an empty clipboard was pushed")
	}
	if s.ShouldApply("", time.Now()) {
		t.Fatal("an empty push was applied")
	}
}

// TestWatcherReportsOnlyChanges. Polling means seeing the same content over and
// over; only the transitions matter.
func TestWatcherReportsOnlyChanges(t *testing.T) {
	var (
		mu       sync.Mutex
		content  = "first"
		reported []string
	)
	w := NewWatcher(WatchConfig{
		Interval: time.Millisecond,
		Read: func(context.Context) (string, error) {
			mu.Lock()
			defer mu.Unlock()
			return content, nil
		},
		OnChange: func(_ context.Context, c string) {
			mu.Lock()
			defer mu.Unlock()
			reported = append(reported, c)
		},
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); w.Run(ctx) }()

	waitFor(t, "the first change", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(reported) == 1
	})

	mu.Lock()
	content = "second"
	mu.Unlock()

	waitFor(t, "the second change", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(reported) == 2
	})

	// Many polls later, still two: the clipboard has not changed again.
	time.Sleep(20 * time.Millisecond)
	cancel()
	<-done

	mu.Lock()
	defer mu.Unlock()
	if len(reported) != 2 || reported[0] != "first" || reported[1] != "second" {
		t.Fatalf("the watcher reported %v", reported)
	}
}

// TestWatcherSurvivesAClipboardItCannotRead: a display that went away is a
// pause, not a crash, and it must not turn into a subprocess per second.
func TestWatcherSurvivesAClipboardItCannotRead(t *testing.T) {
	var (
		mu    sync.Mutex
		reads int
	)
	w := NewWatcher(WatchConfig{
		Interval: time.Millisecond,
		Read: func(context.Context) (string, error) {
			mu.Lock()
			defer mu.Unlock()
			reads++
			return "", ErrNoClipboard
		},
	})

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()
	w.Run(ctx)

	mu.Lock()
	defer mu.Unlock()
	if reads == 0 {
		t.Fatal("the watcher never read")
	}
	// The backoff is 15 seconds, so a 60 ms run can only have read once or
	// twice however fast the loop is.
	if reads > 3 {
		t.Fatalf("a failing clipboard was polled %d times in 60ms; the backoff is not working", reads)
	}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

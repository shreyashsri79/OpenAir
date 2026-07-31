package daemon

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shreyashsri79/openair/internal/caps/clipboard"
	openairv1 "github.com/shreyashsri79/openair/internal/wire/openair/v1"
)

// M12 and M13 through the daemon: what one machine posts appears on the other,
// clearing it anywhere clears it everywhere, and a synced clipboard does not
// bounce.

// eventSink collects daemon events for a watching shell.
type eventSink struct {
	mu     sync.Mutex
	events []*openairv1.DaemonEvent
}

func (s *eventSink) record(ev *openairv1.DaemonEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, ev)
}

func (s *eventSink) of(kind openairv1.DaemonEventKind) []*openairv1.DaemonEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []*openairv1.DaemonEvent
	for _, ev := range s.events {
		if ev.GetKind() == kind {
			out = append(out, ev)
		}
	}
	return out
}

// watching connects a client, subscribes it, and returns the events it sees.
func watching(t *testing.T, d *Daemon) *eventSink {
	t.Helper()
	sink := &eventSink{}
	c := connect(t, d, sink.record, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := c.Subscribe(ctx, false); err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	return sink
}

// TestANotificationAppearsOnTheOtherMachine is M12's point, in the shape PRD
// R23 describes: a long build finishes here, and the person sees it there.
func TestANotificationAppearsOnTheOtherMachine(t *testing.T) {
	source := newTestDaemon(t, nil)
	sink := newTestDaemon(t, nil)
	pinEachOther(t, source, sink)

	events := watching(t, sink)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	sc := connect(t, source, nil, nil)
	delivered, filtered, err := sc.Notify(ctx, sink.Addr(), &openairv1.Posted{
		Key:      "build-1",
		AppId:    "org.example.ci",
		AppName:  "CI",
		Title:    "build finished",
		Body:     "openair: 0 failures",
		Category: "msg",
	})
	if err != nil {
		t.Fatalf("notify: %v", err)
	}
	if filtered || delivered != 1 {
		t.Fatalf("delivered=%d filtered=%v", delivered, filtered)
	}

	waitFor(t, "the notification to arrive", func() bool {
		return len(events.of(openairv1.DaemonEventKind_DAEMON_EVENT_KIND_NOTIFICATION)) == 1
	})
	got := events.of(openairv1.DaemonEventKind_DAEMON_EVENT_KIND_NOTIFICATION)[0].GetNotification()
	if got.GetTitle() != "build finished" || got.GetAppName() != "CI" || got.GetKey() != "build-1" {
		t.Fatalf("arrived as %+v", got)
	}
}

// TestDismissingOnOneMachineClearsItEverywhere. §12's promise, and the reason
// the source tracks which sinks were told.
func TestDismissingOnOneMachineClearsItEverywhere(t *testing.T) {
	source := newTestDaemon(t, nil)
	first := newTestDaemon(t, nil)
	second := newTestDaemon(t, nil)
	pinEachOther(t, source, first)
	pinEachOther(t, source, second)

	firstEvents := watching(t, first)
	secondEvents := watching(t, second)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	sc := connect(t, source, nil, nil)
	for _, target := range []string{first.Addr(), second.Addr()} {
		if _, _, err := sc.Notify(ctx, target, &openairv1.Posted{
			Key: "msg-1", AppId: "org.example.chat", AppName: "Chat", Title: "Ada",
		}); err != nil {
			t.Fatalf("notify %s: %v", target, err)
		}
	}
	waitFor(t, "both machines to show it", func() bool {
		return len(firstEvents.of(openairv1.DaemonEventKind_DAEMON_EVENT_KIND_NOTIFICATION)) == 1 &&
			len(secondEvents.of(openairv1.DaemonEventKind_DAEMON_EVENT_KIND_NOTIFICATION)) == 1
	})

	// A person clears it on the first machine. The source is told, and it tells
	// the second.
	fc := connect(t, first, nil, nil)
	if err := fc.Dismiss(ctx, source.Addr(), "msg-1"); err != nil {
		t.Fatalf("dismiss: %v", err)
	}
	waitFor(t, "the second machine to clear it", func() bool {
		return len(secondEvents.of(openairv1.DaemonEventKind_DAEMON_EVENT_KIND_NOTIFICATION_REMOVED)) == 1
	})
	if got := secondEvents.of(openairv1.DaemonEventKind_DAEMON_EVENT_KIND_NOTIFICATION_REMOVED)[0]; got.GetText() != "msg-1" {
		t.Fatalf("cleared %q", got.GetText())
	}
}

// TestTheFilterRunsBeforeAnythingIsSent (PRD R22). The daemon is where the
// policy lives, so this asserts it there as well as in the capability.
func TestTheFilterRunsBeforeAnythingIsSent(t *testing.T) {
	source := newTestDaemon(t, func(cfg *Config) {
		cfg.NotifyAllow = []string{"org.example.chat"}
	})
	sink := newTestDaemon(t, nil)
	pinEachOther(t, source, sink)

	events := watching(t, sink)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	sc := connect(t, source, nil, nil)
	delivered, filtered, err := sc.Notify(ctx, sink.Addr(), &openairv1.Posted{
		Key: "bank-1", AppId: "com.bank.app", AppName: "Bank", Title: "one-time code 998877",
	})
	if err != nil {
		t.Fatalf("notify: %v", err)
	}
	if !filtered || delivered != 0 {
		t.Fatalf("a filtered notification reported delivered=%d filtered=%v", delivered, filtered)
	}

	// Nothing should arrive, ever. Waiting a moment is the only way to assert
	// an absence, and a moment is enough on loopback.
	time.Sleep(300 * time.Millisecond)
	if got := events.of(openairv1.DaemonEventKind_DAEMON_EVENT_KIND_NOTIFICATION); len(got) != 0 {
		t.Fatalf("a filtered notification arrived anyway: %+v", got)
	}

	// The allowed app still works, so this is a filter rather than an off
	// switch.
	if _, filtered, err := sc.Notify(ctx, sink.Addr(), &openairv1.Posted{
		Key: "chat-1", AppId: "org.example.chat", AppName: "Chat", Title: "hello",
	}); err != nil || filtered {
		t.Fatalf("the allowed app was withheld: %v filtered=%v", err, filtered)
	}
	waitFor(t, "the allowed notification", func() bool {
		return len(events.of(openairv1.DaemonEventKind_DAEMON_EVENT_KIND_NOTIFICATION)) == 1
	})
}

// TestNotifyingEveryConnectedDevice is what `openair notify` with no device
// does, and the reason it is "connected" rather than "paired": a notification
// should not dial six machines.
func TestNotifyingEveryConnectedDevice(t *testing.T) {
	source := newTestDaemon(t, nil)
	a := newTestDaemon(t, nil)
	b := newTestDaemon(t, nil)
	pinEachOther(t, source, a)
	pinEachOther(t, source, b)

	aEvents := watching(t, a)
	bEvents := watching(t, b)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	sc := connect(t, source, nil, nil)
	// Nothing is connected yet, so a broadcast has nowhere to go and says so
	// rather than silently succeeding.
	if _, _, err := sc.Notify(ctx, "", &openairv1.Posted{Key: "k", AppId: "app", Title: "t"}); err == nil {
		t.Fatal("a broadcast with no connected device reported success")
	}

	// Open sessions by sending each device something, then broadcast.
	src := writeFile(t, t.TempDir(), "x.txt", "hello")
	for _, target := range []*Daemon{a, b} {
		target.cfg.AutoAccept = true
		if _, err := sc.Send(ctx, target.Addr(), []string{src}); err != nil {
			t.Fatalf("opening a session: %v", err)
		}
	}

	delivered, _, err := sc.Notify(ctx, "", &openairv1.Posted{
		Key: "k", AppId: "app", AppName: "App", Title: "to everyone",
	})
	if err != nil {
		t.Fatalf("broadcast: %v", err)
	}
	if delivered != 2 {
		t.Fatalf("broadcast reached %d devices, want 2", delivered)
	}
	waitFor(t, "both devices to show it", func() bool {
		return len(aEvents.of(openairv1.DaemonEventKind_DAEMON_EVENT_KIND_NOTIFICATION)) == 1 &&
			len(bEvents.of(openairv1.DaemonEventKind_DAEMON_EVENT_KIND_NOTIFICATION)) == 1
	})
}

// TestAutoClipboardAppliesAPeerPushWithoutSendingItBack is M13's loop
// suppression where it actually matters: through the daemon's receive path.
func TestAutoClipboardAppliesAPeerPushWithoutSendingItBack(t *testing.T) {
	receiver := newTestDaemon(t, func(cfg *Config) { cfg.AutoClipboard = true })
	sender := newTestDaemon(t, nil)
	pinEachOther(t, sender, receiver)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	sc := connect(t, sender, nil, nil)
	if err := sc.Clipboard(ctx, receiver.Addr(), &openairv1.ClipboardPush{
		Mime:      clipboard.TextMIME,
		Content:   []byte("copied on the sender"),
		OriginTs:  time.Now().UnixMilli(),
		OriginTag: string(sender.DeviceID()),
	}); err != nil {
		t.Fatalf("push: %v", err)
	}

	// The receiving daemon recorded it as applied, so its own watcher -- which
	// is what would see the same content on its next poll -- has nothing to
	// send.
	waitFor(t, "the push to be recorded", func() bool {
		return !receiver.clipState.ShouldSend("copied on the sender")
	})

	// And genuinely new local content is still sent.
	if !receiver.clipState.ShouldSend("something typed here") {
		t.Fatal("a local copy was suppressed")
	}
}

// TestAutoClipboardIsOffByDefault. It is the feature most able to surprise
// someone, so the default has to be the quiet one.
func TestAutoClipboardIsOffByDefault(t *testing.T) {
	d := newTestDaemon(t, nil)
	if d.cfg.AutoClipboard {
		t.Fatal("automatic clipboard sync is on by default")
	}

	// The suppression state exists either way, because the receive path uses it
	// whether or not the watcher runs.
	if d.clipState == nil {
		t.Fatal("no clipboard state without the watcher")
	}
}

// TestNotifyRefusesAKeylessNotification, because the key is what makes a
// dismissal addressable.
func TestNotifyRefusesAKeylessNotification(t *testing.T) {
	source := newTestDaemon(t, nil)
	sink := newTestDaemon(t, nil)
	pinEachOther(t, source, sink)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	sc := connect(t, source, nil, nil)
	_, _, err := sc.Notify(ctx, sink.Addr(), &openairv1.Posted{AppId: "app", Title: "no key"})
	if err == nil || !strings.Contains(err.Error(), "key") {
		t.Fatalf("a keyless notification returned %v", err)
	}
}

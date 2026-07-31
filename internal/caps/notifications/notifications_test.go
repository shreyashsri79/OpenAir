package notifications

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/shreyashsri79/openair/internal/caps"
	"github.com/shreyashsri79/openair/internal/identity"
	"github.com/shreyashsri79/openair/internal/session"
	openairv1 "github.com/shreyashsri79/openair/internal/wire/openair/v1"
)

var _ caps.Capability = (*Capability)(nil)

// spySession records what was sent and delivers it to a peer capability, so a
// test can assert on the bytes that crossed as well as on what arrived. The
// first half is what PRD R22 needs: filtering is only real if the excluded
// content never reaches the wire.
type spySession struct {
	peer   identity.Peer
	remote *Capability
	// remotePeer is who the far end sees this session as.
	remotePeer identity.Peer

	mu   sync.Mutex
	sent []*openairv1.Posted
	raw  []sentMsg
}

type sentMsg struct {
	msgType uint16
	payload []byte
}

func (s *spySession) Peer() identity.Peer        { return s.peer }
func (s *spySession) SendDatagram([]byte) error  { return errors.New("spy: no datagrams") }
func (s *spySession) PathInfo() session.PathInfo { return session.PathInfo{Class: "lan"} }
func (s *spySession) Close(uint16, string) error { return nil }
func (s *spySession) Done() <-chan struct{}      { return nil }
func (s *spySession) Quiesce(context.Context, uint32, string) (func(), error) {
	return func() {}, nil
}
func (s *spySession) OpenStream(context.Context) (session.Stream, error) {
	return nil, errors.New("spy: §12 opens no streams")
}

func (s *spySession) Send(ctx context.Context, capID byte, msgType uint16, msg proto.Message) error {
	b, err := proto.Marshal(msg)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.raw = append(s.raw, sentMsg{msgType: msgType, payload: b})
	if msgType == MsgPosted {
		var p openairv1.Posted
		if err := proto.Unmarshal(b, &p); err == nil {
			s.sent = append(s.sent, &p)
		}
	}
	s.mu.Unlock()

	if s.remote == nil {
		return nil
	}
	// Delivered inline, like the control loop does: one message at a time, in
	// order.
	return s.remote.Serve(ctx, &spySession{peer: s.remotePeer}, msgType, b)
}

func (s *spySession) wire() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	var all []byte
	for _, m := range s.raw {
		all = append(all, m.payload...)
	}
	return all
}

func (s *spySession) posted() []*openairv1.Posted {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]*openairv1.Posted(nil), s.sent...)
}

func (s *spySession) types() []uint16 {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]uint16, 0, len(s.raw))
	for _, m := range s.raw {
		out = append(out, m.msgType)
	}
	return out
}

func peerID(t *testing.T) identity.Peer {
	t.Helper()
	id, err := identity.LoadOrCreate(identity.Options{Dir: t.TempDir(), Tier: identity.TierNone})
	if err != nil {
		t.Fatal(err)
	}
	return identity.Peer{DeviceID: id.DeviceID(), DisplayName: "peer"}
}

// TestMirroringANotification is the milestone in one test: what one device
// posts, another shows.
func TestMirroringANotification(t *testing.T) {
	var got []Notification
	sink := New(Config{
		OnPosted: func(_ context.Context, _ identity.Peer, n Notification) { got = append(got, n) },
	})
	source := New(Config{})
	sess := &spySession{peer: peerID(t), remote: sink, remotePeer: peerID(t)}

	n := Notification{
		Key:      "k1",
		AppID:    "org.example.chat",
		AppName:  "Chat",
		Title:    "Ada",
		Body:     "the analytical engine is ready",
		Category: CategoryMessage,
		Actions:  []Action{{ID: "reply", Label: "Reply", AcceptsText: true}},
	}
	if err := source.Post(context.Background(), sess, n); err != nil {
		t.Fatalf("post: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("the sink saw %d notifications", len(got))
	}
	if got[0].Title != "Ada" || got[0].AppName != "Chat" || len(got[0].Actions) != 1 {
		t.Fatalf("arrived as %+v", got[0])
	}
	if !got[0].Actions[0].AcceptsText {
		t.Fatal("the inline-reply flag did not survive")
	}
	if got[0].PostedAt.IsZero() {
		t.Fatal("posted_at was not filled in")
	}
}

// TestFilteredContentNeverReachesTheWire is PRD R22 stated as an assertion:
// the check is not "was it delivered" but "did the bytes exist at all".
func TestFilteredContentNeverReachesTheWire(t *testing.T) {
	var got []Notification
	sink := New(Config{
		OnPosted: func(_ context.Context, _ identity.Peer, n Notification) { got = append(got, n) },
	})
	source := New(Config{Allow: AllowList("org.example.chat")})
	sess := &spySession{peer: peerID(t), remote: sink, remotePeer: peerID(t)}

	secret := Notification{
		Key:     "k-bank",
		AppID:   "com.bank.app",
		AppName: "Bank",
		Title:   "Your balance is 4",
		Body:    "one-time code 998877",
	}
	err := source.Post(context.Background(), sess, secret)
	if !errors.Is(err, ErrFiltered) {
		t.Fatalf("a filtered notification returned %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("a filtered notification was delivered: %+v", got)
	}
	if wire := sess.wire(); bytes.Contains(wire, []byte("998877")) || bytes.Contains(wire, []byte("Bank")) {
		t.Fatal("filtered content appeared on the wire")
	}
	if len(sess.types()) != 0 {
		t.Fatalf("a filtered notification still sent %v", sess.types())
	}

	// And the allowed app still goes through, so the filter is a filter and not
	// an off switch.
	if err := source.Post(context.Background(), sess, Notification{
		Key: "k1", AppID: "org.example.chat", Title: "hello",
	}); err != nil {
		t.Fatalf("an allowed notification was refused: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("the allowed notification did not arrive")
	}
}

func TestBlockList(t *testing.T) {
	c := New(Config{Allow: BlockList("com.bank.app")})
	if c.Allowed("com.bank.app") {
		t.Fatal("a blocked app was allowed")
	}
	if c.Allowed("COM.BANK.APP") {
		t.Fatal("the block list is case-sensitive, so it can be bypassed by case")
	}
	if !c.Allowed("org.example.chat") {
		t.Fatal("a block list excluded an app it does not name")
	}
	// No filter at all allows everything, which is the desktop default.
	if !New(Config{}).Allowed("anything") {
		t.Fatal("the zero config filtered a notification")
	}
}

// TestDismissFlowsBackToTheSource: §12's dismiss-on-one-dismisses-everywhere
// starts here, with the sink telling the source.
func TestDismissFlowsBackToTheSource(t *testing.T) {
	var dismissed []string
	source := New(Config{
		OnDismiss: func(_ context.Context, _ identity.Peer, key string) { dismissed = append(dismissed, key) },
	})
	sink := New(Config{})
	toSource := &spySession{peer: peerID(t), remote: source, remotePeer: peerID(t)}

	if err := sink.Dismiss(context.Background(), toSource, "k1"); err != nil {
		t.Fatalf("dismiss: %v", err)
	}
	if len(dismissed) != 1 || dismissed[0] != "k1" {
		t.Fatalf("the source saw %v", dismissed)
	}
	if err := sink.Dismiss(context.Background(), toSource, ""); !errors.Is(err, ErrNoKey) {
		t.Fatalf("a keyless dismiss returned %v", err)
	}
}

// TestTheSourceKnowsWhichSinksHaveAKey is what makes the "everywhere" half
// possible: the source has to know who to tell.
func TestTheSourceKnowsWhichSinksHaveAKey(t *testing.T) {
	source := New(Config{})
	a, b := peerID(t), peerID(t)
	sessA := &spySession{peer: a}
	sessB := &spySession{peer: b}

	n := Notification{Key: "k1", AppID: "app", Title: "hello"}
	for _, sess := range []*spySession{sessA, sessB} {
		if err := source.Post(context.Background(), sess, n); err != nil {
			t.Fatal(err)
		}
	}
	if got := source.Sinks("k1"); len(got) != 2 {
		t.Fatalf("the source recorded %v", got)
	}

	// Removing from one leaves the other.
	if err := source.Remove(context.Background(), sessA, "k1"); err != nil {
		t.Fatal(err)
	}
	got := source.Sinks("k1")
	if len(got) != 1 || got[0] != b.DeviceID {
		t.Fatalf("after removing from one sink: %v", got)
	}
	source.Forget("k1")
	if got := source.Sinks("k1"); len(got) != 0 {
		t.Fatalf("Forget left %v", got)
	}
}

// TestARemovedForAnUnknownKeyIsTolerated, which §12 requires in as many words.
func TestARemovedForAnUnknownKeyIsTolerated(t *testing.T) {
	var removed []string
	sink := New(Config{
		OnRemoved: func(_ context.Context, _ identity.Peer, key string) { removed = append(removed, key) },
	})
	payload, err := proto.Marshal(&openairv1.Removed{Key: "never-seen"})
	if err != nil {
		t.Fatal(err)
	}
	if err := sink.Serve(context.Background(), &spySession{peer: peerID(t)}, MsgRemoved, payload); err != nil {
		t.Fatalf("a Removed for an unknown key failed: %v", err)
	}
	if len(removed) != 1 || removed[0] != "never-seen" {
		t.Fatalf("removed = %v", removed)
	}
}

// TestActionsCarryInlineReplies (PRD R22's fast-follow, and K7's quirky half).
func TestActionsCarryInlineReplies(t *testing.T) {
	type invocation struct{ key, action, text string }
	var got []invocation
	source := New(Config{
		OnAction: func(_ context.Context, _ identity.Peer, key, actionID, text string) {
			got = append(got, invocation{key, actionID, text})
		},
	})
	sink := New(Config{})
	sess := &spySession{peer: peerID(t), remote: source, remotePeer: peerID(t)}

	if err := sink.InvokeAction(context.Background(), sess, "k1", "reply", "on my way"); err != nil {
		t.Fatalf("invoke: %v", err)
	}
	if len(got) != 1 || got[0].text != "on my way" || got[0].action != "reply" {
		t.Fatalf("the source saw %+v", got)
	}
	if err := sink.InvokeAction(context.Background(), sess, "k1", "", "x"); err == nil {
		t.Fatal("an action with no id was accepted")
	}
}

// TestOversizedContentIsRefused, on both sides: a peer is not obliged to have
// been built from this source tree.
func TestOversizedContentIsRefused(t *testing.T) {
	source := New(Config{})
	sess := &spySession{peer: peerID(t)}

	big := Notification{Key: "k", AppID: "app", Title: strings.Repeat("x", DefaultMaxTextBytes+1)}
	if err := source.Post(context.Background(), sess, big); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("an oversized title returned %v", err)
	}
	icon := Notification{Key: "k", AppID: "app", Title: "t", IconPNG: make([]byte, DefaultMaxIconBytes+1)}
	if err := source.Post(context.Background(), sess, icon); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("an oversized icon returned %v", err)
	}

	var seen int
	sink := New(Config{OnPosted: func(context.Context, identity.Peer, Notification) { seen++ }})
	payload, err := proto.Marshal(&openairv1.Posted{
		Key:   "k",
		AppId: "app",
		Body:  strings.Repeat("y", DefaultMaxTextBytes+1),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := sink.Serve(context.Background(), sess, MsgPosted, payload); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("an oversized inbound body returned %v", err)
	}
	if seen != 0 {
		t.Fatal("an oversized notification was shown")
	}
}

// TestNotificationsRunOnTheIdentityKey pins the level, for the same reason the
// clipboard's test does: raising it to Owned would stop notifications the
// moment an unlock lapsed (D-20, D-75).
func TestNotificationsRunOnTheIdentityKey(t *testing.T) {
	if got := New(Config{}).RequiredLevel(); got != identity.LevelTrusted {
		t.Fatalf("RequiredLevel = %v, want Trusted", got)
	}
}

func TestAKeylessNotificationIsRefused(t *testing.T) {
	source := New(Config{})
	sess := &spySession{peer: peerID(t)}
	err := source.Post(context.Background(), sess, Notification{AppID: "app", Title: "no key"})
	if !errors.Is(err, ErrNoKey) {
		t.Fatalf("a keyless notification returned %v", err)
	}
	if len(sess.types()) != 0 {
		t.Fatal("a keyless notification still sent something")
	}
}

func TestPostedAtSurvivesTheRoundTrip(t *testing.T) {
	when := time.Now().Add(-3 * time.Minute).Truncate(time.Millisecond)
	var got Notification
	sink := New(Config{OnPosted: func(_ context.Context, _ identity.Peer, n Notification) { got = n }})
	source := New(Config{})
	sess := &spySession{peer: peerID(t), remote: sink, remotePeer: peerID(t)}

	if err := source.Post(context.Background(), sess, Notification{
		Key: "k", AppID: "app", Title: "t", PostedAt: when,
	}); err != nil {
		t.Fatal(err)
	}
	if !got.PostedAt.Equal(when) {
		t.Fatalf("posted_at came back as %s, want %s", got.PostedAt, when)
	}
}

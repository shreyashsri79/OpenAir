package clipboard

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"google.golang.org/protobuf/proto"

	"github.com/shreyashsri79/openair/internal/identity"
	"github.com/shreyashsri79/openair/internal/session"
	openairv1 "github.com/shreyashsri79/openair/internal/wire/openair/v1"
)

// fakeSession records what a capability sends and delivers it to the far side.
// The clipboard is one control message with no stream and no reply, so this is
// all the transport the tests need.
type fakeSession struct {
	peer identity.Peer

	mu   sync.Mutex
	sent []*openairv1.ClipboardPush
}

func (s *fakeSession) Peer() identity.Peer { return s.peer }
func (s *fakeSession) OpenStream(context.Context) (session.Stream, error) {
	return nil, errors.New("not used")
}
func (s *fakeSession) SendDatagram([]byte) error  { return errors.New("not used") }
func (s *fakeSession) PathInfo() session.PathInfo { return session.PathInfo{Class: "lan"} }
func (s *fakeSession) Close(uint16, string) error { return nil }
func (s *fakeSession) Done() <-chan struct{}      { return nil }
func (s *fakeSession) Quiesce(context.Context, uint32, string) (func(), error) {
	return func() {}, nil
}

func (s *fakeSession) Send(_ context.Context, capID byte, msgType uint16, msg proto.Message) error {
	if capID != CapID {
		return errors.New("wrong capID")
	}
	if msgType != msgPush {
		return errors.New("wrong msgType")
	}
	b, err := proto.Marshal(msg)
	if err != nil {
		return err
	}
	var p openairv1.ClipboardPush
	if err := proto.Unmarshal(b, &p); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sent = append(s.sent, &p)
	return nil
}

func (s *fakeSession) last(t *testing.T) *openairv1.ClipboardPush {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.sent) == 0 {
		t.Fatal("nothing was sent")
	}
	return s.sent[len(s.sent)-1]
}

// deliver hands what a sender produced to a receiving capability, as the
// session layer would.
func deliver(t *testing.T, recv *Capability, sess *fakeSession, push *openairv1.ClipboardPush) error {
	t.Helper()
	payload, err := proto.Marshal(push)
	if err != nil {
		t.Fatal(err)
	}
	return recv.Serve(context.Background(), sess, msgPush, payload)
}

// TestRoundTripUTF8 covers what a clipboard actually carries: accents, CJK,
// emoji with combining sequences, and a newline. A capability that mangles any
// of these is worse than one that refuses them, because the user finds out by
// pasting.
func TestRoundTripUTF8(t *testing.T) {
	cases := []string{
		"hello",
		"café — naïve",
		"日本語のテキスト",
		"👩‍💻 👨‍👩‍👧‍👦 🇯🇵",
		"line one\nline two\ttabbed",
		"",
	}

	for _, want := range cases {
		var got Content
		recv := New(Config{OnReceive: func(_ context.Context, _ identity.Peer, c Content) error {
			got = c
			return nil
		}})
		send := New(Config{Tag: "sender"})
		sess := &fakeSession{peer: identity.Peer{DeviceID: "sender0000000000"}}

		if err := send.PushText(context.Background(), sess, want); err != nil {
			t.Fatalf("PushText(%q): %v", want, err)
		}
		if err := deliver(t, recv, sess, sess.last(t)); err != nil {
			t.Fatalf("Serve(%q): %v", want, err)
		}
		if got.Text() != want {
			t.Errorf("round trip = %q, want %q", got.Text(), want)
		}
		if got.MIME != TextMIME {
			t.Errorf("mime = %q, want %q", got.MIME, TextMIME)
		}
		if got.OriginTag != "sender" {
			t.Errorf("origin tag = %q, want sender", got.OriginTag)
		}
	}
}

// TestOversizedIsRejectedNotBuffered is §9's rule. Both ends enforce it: a
// sender that would have to be told no does not send, and a receiver refuses
// without applying anything.
func TestOversizedIsRejectedNotBuffered(t *testing.T) {
	const max = 64
	big := strings.Repeat("x", max+1)

	send := New(Config{MaxBytes: max})
	sess := &fakeSession{}
	err := send.PushText(context.Background(), sess, big)
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("PushText error = %v, want ErrTooLarge", err)
	}
	sess.mu.Lock()
	n := len(sess.sent)
	sess.mu.Unlock()
	if n != 0 {
		t.Error("oversized content was put on the wire anyway")
	}

	applied := false
	recv := New(Config{MaxBytes: max, OnReceive: func(context.Context, identity.Peer, Content) error {
		applied = true
		return nil
	}})
	err = deliver(t, recv, sess, &openairv1.ClipboardPush{Mime: TextMIME, Content: []byte(big)})
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("Serve error = %v, want ErrTooLarge", err)
	}
	if applied {
		t.Error("oversized content reached OnReceive")
	}
}

func TestAtTheCapExactlyIsAccepted(t *testing.T) {
	const max = 64
	exact := strings.Repeat("x", max)

	got := ""
	recv := New(Config{MaxBytes: max, OnReceive: func(_ context.Context, _ identity.Peer, c Content) error {
		got = c.Text()
		return nil
	}})
	if err := deliver(t, recv, &fakeSession{}, &openairv1.ClipboardPush{Mime: TextMIME, Content: []byte(exact)}); err != nil {
		t.Fatalf("Serve: %v", err)
	}
	if got != exact {
		t.Errorf("content at exactly the cap was not delivered intact")
	}
}

// TestNonTextIsRefused: this build sends and accepts text. Images are §9's
// stated later work, and accepting bytes we cannot interpret would put
// arbitrary content into a user's clipboard.
func TestNonTextIsRefused(t *testing.T) {
	recv := New(Config{OnReceive: func(context.Context, identity.Peer, Content) error {
		t.Error("non-text content reached OnReceive")
		return nil
	}})
	err := deliver(t, recv, &fakeSession{}, &openairv1.ClipboardPush{
		Mime: "image/png", Content: []byte{0x89, 'P', 'N', 'G'},
	})
	if !errors.Is(err, ErrUnsupportedMIME) {
		t.Fatalf("Serve error = %v, want ErrUnsupportedMIME", err)
	}
}

// TestInvalidUTF8IsRefused: the MIME type names a charset, and content that is
// not that charset is either a bug or an attempt to smuggle bytes past a
// text-only path.
func TestInvalidUTF8IsRefused(t *testing.T) {
	recv := New(Config{})
	err := deliver(t, recv, &fakeSession{}, &openairv1.ClipboardPush{
		Mime: TextMIME, Content: []byte{0xff, 0xfe, 0xfd},
	})
	if !errors.Is(err, ErrNotText) {
		t.Fatalf("Serve error = %v, want ErrNotText", err)
	}
}

// TestBareTextPlainIsAccepted: a conformant sender may omit the charset
// parameter, and refusing it would be this implementation reading §9 more
// strictly than §9 is written.
func TestBareTextPlainIsAccepted(t *testing.T) {
	got := ""
	recv := New(Config{OnReceive: func(_ context.Context, _ identity.Peer, c Content) error {
		got = c.Text()
		return nil
	}})
	if err := deliver(t, recv, &fakeSession{}, &openairv1.ClipboardPush{
		Mime: "text/plain", Content: []byte("bare"),
	}); err != nil {
		t.Fatalf("Serve: %v", err)
	}
	if got != "bare" {
		t.Errorf("content = %q, want bare", got)
	}
}

// TestUnknownMessageTypeIsIgnored is §3.1: unknown types within a known
// capability are non-fatal, and only the capability can say so.
func TestUnknownMessageTypeIsIgnored(t *testing.T) {
	recv := New(Config{OnReceive: func(context.Context, identity.Peer, Content) error {
		t.Error("an unknown message type reached OnReceive")
		return nil
	}})
	err := recv.Serve(context.Background(), &fakeSession{}, 9999, nil)
	if !errors.Is(err, session.ErrUnknownMsgType) {
		t.Fatalf("Serve error = %v, want ErrUnknownMsgType", err)
	}
}

// TestOwnEchoIsDropped is M13's rule, enforced now because the field exists
// now: a push carrying this device's own tag came from here.
func TestOwnEchoIsDropped(t *testing.T) {
	applied := false
	recv := New(Config{Tag: "device-a", OnReceive: func(context.Context, identity.Peer, Content) error {
		applied = true
		return nil
	}})
	if err := deliver(t, recv, &fakeSession{}, &openairv1.ClipboardPush{
		Mime: TextMIME, Content: []byte("mine"), OriginTag: "device-a",
	}); err != nil {
		t.Fatalf("Serve: %v", err)
	}
	if applied {
		t.Error("a device applied its own clipboard push")
	}
}

// TestRunsOnTheIdentityKey pins D-20's consequence in a test rather than only
// in prose: raising this to Owned would stop the clipboard working whenever an
// unlock expired.
func TestRunsOnTheIdentityKey(t *testing.T) {
	if got := New(Config{}).RequiredLevel(); got != identity.LevelTrusted {
		t.Fatalf("RequiredLevel = %v, want LevelTrusted (D-20)", got)
	}
}

func TestCapIDIsTwo(t *testing.T) {
	if New(Config{}).CapID() != 0x02 {
		t.Fatal("capID must be 2 (PROTOCOL.md Appendix B)")
	}
}

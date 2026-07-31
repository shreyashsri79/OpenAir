package clipboard

import (
	"context"
	"errors"
	"fmt"
	"time"
	"unicode/utf8"

	"google.golang.org/protobuf/proto"

	"github.com/shreyashsri79/openair/internal/identity"
	"github.com/shreyashsri79/openair/internal/session"
	openairv1 "github.com/shreyashsri79/openair/internal/wire/openair/v1"
)

// CapID is the clipboard capability's wire ID (PROTOCOL.md Appendix B).
const CapID byte = 0x02

// msgPush is the only message type this capability defines (§9). Like every
// other *MessageType enum, its values are the wire values with 0 invalid
// (D-39) -- no offset applies.
const msgPush = uint16(openairv1.ClipboardMessageType_CLIPBOARD_MESSAGE_TYPE_PUSH)

// TextMIME is what Phase 1 sends. §9 leaves the field open for images later.
const TextMIME = "text/plain; charset=utf-8"

// DefaultMaxBytes caps accepted content.
//
// §9 says receivers SHOULD cap and reject rather than buffer. One MiB is far
// more than any clipboard a person produces by selecting text, and far less
// than the envelope's 16 MiB ceiling, so a peer cannot use a clipboard push to
// make a receiver hold a large allocation. It is worth being precise about what
// this does and does not do: the envelope layer has already read the payload by
// the time a capability sees it, so the 16 MiB envelope cap is what bounds
// allocation, and this cap is what stops oversized content being applied,
// stored, or written to a clipboard nobody asked to be filled.
const DefaultMaxBytes = 1 << 20

var (
	// ErrTooLarge is a §9 refusal, mapped to RESOURCE_EXHAUSTED.
	ErrTooLarge = errors.New("clipboard: content exceeds the accepted size")
	// ErrUnsupportedMIME is content this build will not apply.
	ErrUnsupportedMIME = errors.New("clipboard: unsupported content type")
	// ErrNotText is content claiming to be UTF-8 text and failing to be.
	ErrNotText = errors.New("clipboard: content is not valid UTF-8")
)

// Content is one received clipboard push, after validation.
type Content struct {
	MIME      string
	Bytes     []byte
	OriginTS  time.Time
	OriginTag string
}

// Text returns the content as a string. Phase 1 is text only, so this is the
// accessor callers actually want.
func (c Content) Text() string { return string(c.Bytes) }

// Config configures the capability. The zero value works and does nothing with
// what it receives, which is the right default for a process with no clipboard
// to write to.
type Config struct {
	// MaxBytes caps accepted content. Zero means DefaultMaxBytes.
	MaxBytes int

	// OnReceive is called with each accepted push. Returning an error refuses
	// it -- the operation fails for the peer, and nothing is applied locally.
	OnReceive func(ctx context.Context, peer identity.Peer, c Content) error

	// Tag identifies this device as the origin of its own pushes, so an
	// auto-sync implementation (M13) can suppress the echo. Empty is fine for
	// manual push, which has no loop to break.
	Tag string
}

func (c Config) maxBytes() int {
	if c.MaxBytes <= 0 {
		return DefaultMaxBytes
	}
	return c.MaxBytes
}

// Capability is the clipboard capability (§9).
//
// It runs on the identity key, not the privilege key (D-20). Gating it would
// have stopped clipboard working whenever an unlock expired, which would put
// the auth policy in front of a user in exactly the place they would least
// tolerate it -- and they would blame the feature rather than the policy.
type Capability struct {
	cfg Config
}

func New(cfg Config) *Capability { return &Capability{cfg: cfg} }

func (c *Capability) CapID() byte { return CapID }

// RequiredLevel is Trusted: the level a device has the moment it is paired.
// See the type comment for why this is not Owned.
func (c *Capability) RequiredLevel() identity.TrustLevel { return identity.LevelTrusted }

// Push sends local clipboard content to a peer.
func (c *Capability) Push(ctx context.Context, sess session.Session, mime string, content []byte) error {
	if mime == "" {
		mime = TextMIME
	}
	if err := validate(mime, content, c.cfg.maxBytes()); err != nil {
		return err
	}
	return sess.Send(ctx, CapID, msgPush, &openairv1.ClipboardPush{
		Mime:      mime,
		Content:   content,
		OriginTs:  time.Now().UnixMilli(),
		OriginTag: c.cfg.Tag,
	})
}

// PushText is Push for the Phase 1 case.
func (c *Capability) PushText(ctx context.Context, sess session.Session, text string) error {
	return c.Push(ctx, sess, TextMIME, []byte(text))
}

// Serve handles an inbound push.
func (c *Capability) Serve(ctx context.Context, sess session.Session, msgType uint16, payload []byte) error {
	if msgType != msgPush {
		// §3.1: a message type this build does not know is ignored, never
		// fatal. Returning this sentinel is how the session layer is told.
		return session.ErrUnknownMsgType
	}

	var m openairv1.ClipboardPush
	if err := proto.Unmarshal(payload, &m); err != nil {
		return fmt.Errorf("clipboard: malformed push: %w", err)
	}

	mime := m.GetMime()
	if mime == "" {
		mime = TextMIME
	}
	if err := validate(mime, m.GetContent(), c.cfg.maxBytes()); err != nil {
		return err
	}

	// A push carrying this device's own tag is an echo of something it sent.
	// Manual push never produces one; auto-sync (M13) will, and dropping it
	// here means that milestone does not have to re-derive the rule.
	if c.cfg.Tag != "" && m.GetOriginTag() == c.cfg.Tag {
		return nil
	}

	if c.cfg.OnReceive == nil {
		return nil
	}
	return c.cfg.OnReceive(ctx, sess.Peer(), Content{
		MIME:      mime,
		Bytes:     m.GetContent(),
		OriginTS:  time.UnixMilli(m.GetOriginTs()),
		OriginTag: m.GetOriginTag(),
	})
}

// ServeStream is never called: clipboard content travels on the control
// stream, and a peer opening a capability stream for capID 2 has misread §9.
func (c *Capability) ServeStream(_ context.Context, _ session.Session, st session.Stream, _ uint16, _ []byte) error {
	st.Reset(uint32(session.CodeProtocolViolation))
	return fmt.Errorf("clipboard: content travels on the control stream, not a capability stream")
}

// validate applies the two rules §9 states and the one UTF-8 implies.
func validate(mime string, content []byte, max int) error {
	if len(content) > max {
		return fmt.Errorf("%w: %d bytes, cap is %d", ErrTooLarge, len(content), max)
	}
	if !isText(mime) {
		return fmt.Errorf("%w: %q; this build sends and accepts text only", ErrUnsupportedMIME, mime)
	}
	if !utf8.Valid(content) {
		// The MIME type names a charset. Content that is not that charset is a
		// bug or an attempt to smuggle bytes past a text-only path, and
		// applying it would put invalid UTF-8 into someone's clipboard.
		return ErrNotText
	}
	return nil
}

// isText reports whether a MIME type is the text/plain family §9 defines. The
// parameters after the semicolon are not compared: "text/plain" and
// "text/plain; charset=utf-8" are the same thing to a peer that only speaks
// UTF-8, and refusing the shorter form would reject a conformant sender.
func isText(mime string) bool {
	base := mime
	for i := 0; i < len(mime); i++ {
		if mime[i] == ';' {
			base = mime[:i]
			break
		}
	}
	for len(base) > 0 && base[len(base)-1] == ' ' {
		base = base[:len(base)-1]
	}
	return base == "text/plain"
}

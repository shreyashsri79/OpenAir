// Package notifications is PROTOCOL.md §12: mirroring notifications from a
// source device — usually a phone — to the machines a person is sitting at
// (capID 4, PRD R21–R23).
//
// # Filtering is on the source, and that is the whole privacy design
//
// §12 puts the per-app allowlist on the source, evaluated before a `Posted` is
// constructed, so excluded content never crosses the wire and never reaches a
// relay. There is deliberately no filter-configuration message: a filter
// negotiated over the wire would mean the filtered content had already left the
// device that was supposed to withhold it. `Config.Allow` is that policy, and
// `Post` applies it before marshalling anything (D-76).
//
// # It runs on the identity key
//
// Notifications mirror continuously in the background, which puts them in the
// same class as the clipboard (D-20): raising them to Owned would stop the
// mirroring whenever an unlock lapsed, and a person would experience that as
// the feature being broken rather than as a policy being enforced. What
// protects the content is the source-side filter, plus the fact that a peer has
// to be paired at all.
package notifications

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/shreyashsri79/openair/internal/identity"
	"github.com/shreyashsri79/openair/internal/session"
	openairv1 "github.com/shreyashsri79/openair/internal/wire/openair/v1"
)

// CapID is the notifications capability's wire ID (Appendix B).
const CapID byte = 0x04

// Message types (§12). Wire values, no offset (D-39).
const (
	MsgPosted       = uint16(openairv1.NotificationsMessageType_NOTIFICATIONS_MESSAGE_TYPE_POSTED)
	MsgRemoved      = uint16(openairv1.NotificationsMessageType_NOTIFICATIONS_MESSAGE_TYPE_REMOVED)
	MsgDismiss      = uint16(openairv1.NotificationsMessageType_NOTIFICATIONS_MESSAGE_TYPE_DISMISS)
	MsgActionInvoke = uint16(openairv1.NotificationsMessageType_NOTIFICATIONS_MESSAGE_TYPE_ACTION_INVOKE)
)

const (
	// DefaultMaxIconBytes caps the icon a source may send. §12 says sources
	// SHOULD cap to 64x64; a PNG that size is a couple of kilobytes, so this is
	// generous and exists to bound a peer rather than to be reached.
	DefaultMaxIconBytes = 64 << 10

	// DefaultMaxTextBytes caps title and body each. A notification is a
	// glanceable thing; anything past this is either a bug on the source or an
	// attempt to use the sink as storage.
	DefaultMaxTextBytes = 8 << 10

	// DefaultMaxActions caps the buttons on one notification.
	DefaultMaxActions = 8
)

// Categories §12 defines. Anything else is carried through unchanged: a sink
// that does not recognise a category shows the notification anyway.
const (
	CategoryMessage  = "msg"
	CategoryCall     = "call"
	CategoryAlarm    = "alarm"
	CategoryProgress = "progress"
)

var (
	// ErrTooLarge is a notification past one of the caps.
	ErrTooLarge = errors.New("notifications: content exceeds the accepted size")

	// ErrFiltered reports a notification the source's own policy excluded. It
	// is not a failure: it is the filter working, and Post reports it so a
	// caller can count what it withheld rather than wonder.
	ErrFiltered = errors.New("notifications: withheld by this device's filter")

	// ErrNoKey is a notification with no key. The key is what makes dismissal
	// and removal addressable, so a notification without one cannot be
	// mirrored coherently.
	ErrNoKey = errors.New("notifications: a notification needs a key")
)

// Action is one button on a notification.
type Action struct {
	ID          string
	Label       string
	AcceptsText bool // inline reply
}

// Notification is one posted notification, in Go terms.
type Notification struct {
	Key      string
	AppID    string
	AppName  string
	Title    string
	Body     string
	IconPNG  []byte
	PostedAt time.Time
	Category string
	Actions  []Action
}

// Config configures the capability.
//
// The zero value mirrors nothing outbound and does nothing with what arrives,
// which is right for a device that has not been asked to do either.
type Config struct {
	// Allow is the source-side filter (PRD R22). It is called with the app ID
	// before a notification is marshalled, so a notification it refuses never
	// exists on the wire. Nil allows everything, which is the correct default
	// for a desktop forwarding its own build-finished notifications (R23) and
	// the wrong one for a phone -- hence AllowList and BlockList below.
	Allow func(appID string) bool

	// OnPosted receives a notification from a peer. Nil means this device is
	// not a sink, and inbound notifications are dropped rather than queued.
	OnPosted func(ctx context.Context, peer identity.Peer, n Notification)

	// OnRemoved is called when the source says a notification is gone. §12
	// requires sinks to tolerate a Removed for a key they never saw, so this
	// may be called for something never shown.
	OnRemoved func(ctx context.Context, peer identity.Peer, key string)

	// OnDismiss is the source side of dismiss-on-one-dismisses-everywhere: a
	// sink dismissed this device's notification, and this device should clear
	// it and tell the other sinks.
	OnDismiss func(ctx context.Context, peer identity.Peer, key string)

	// OnAction is a sink invoking a notification's button, with text for an
	// inline reply.
	OnAction func(ctx context.Context, peer identity.Peer, key, actionID, text string)

	MaxIconBytes int
	MaxTextBytes int
	MaxActions   int
}

// AllowList builds a filter that permits only these app IDs. It is the shape a
// phone wants: everything is private until named.
func AllowList(apps ...string) func(string) bool {
	set := make(map[string]struct{}, len(apps))
	for _, a := range apps {
		set[strings.ToLower(a)] = struct{}{}
	}
	return func(appID string) bool {
		_, ok := set[strings.ToLower(appID)]
		return ok
	}
}

// BlockList builds a filter that permits everything except these app IDs.
//
// Weaker than an allowlist and worth saying why it exists anyway: a desktop
// forwarding its own notifications knows what it runs, and being made to
// enumerate every application before anything works would mean nobody turns it
// on. A phone should use AllowList.
func BlockList(apps ...string) func(string) bool {
	set := make(map[string]struct{}, len(apps))
	for _, a := range apps {
		set[strings.ToLower(a)] = struct{}{}
	}
	return func(appID string) bool {
		_, blocked := set[strings.ToLower(appID)]
		return !blocked
	}
}

// Capability is the notifications capability (§12).
//
// It is symmetric on purpose: the same object is the source for what this
// device posts and the sink for what it receives, because §12's messages are
// the same in both directions and desktop-to-desktop forwarding (R23) is just
// the phone case with a different sender.
type Capability struct {
	cfg Config

	mu sync.Mutex
	// posted tracks which peers were told about each key this device posted,
	// so that a dismiss arriving from one sink can be forwarded to the others.
	posted map[string]map[identity.DeviceID]struct{}
}

func New(cfg Config) *Capability {
	return &Capability{cfg: cfg, posted: map[string]map[identity.DeviceID]struct{}{}}
}

func (c *Capability) CapID() byte { return CapID }

// RequiredLevel is Trusted. See the package comment: gating this on Owned would
// stop notifications mirroring whenever an unlock lapsed (D-20, D-75).
func (c *Capability) RequiredLevel() identity.TrustLevel { return identity.LevelTrusted }

func (c *Capability) maxIcon() int {
	if c.cfg.MaxIconBytes > 0 {
		return c.cfg.MaxIconBytes
	}
	return DefaultMaxIconBytes
}

func (c *Capability) maxText() int {
	if c.cfg.MaxTextBytes > 0 {
		return c.cfg.MaxTextBytes
	}
	return DefaultMaxTextBytes
}

func (c *Capability) maxActions() int {
	if c.cfg.MaxActions > 0 {
		return c.cfg.MaxActions
	}
	return DefaultMaxActions
}

// Allowed reports whether this device's filter would let a notification from
// appID out. It is exported so a caller can skip the work of building one.
func (c *Capability) Allowed(appID string) bool {
	return c.cfg.Allow == nil || c.cfg.Allow(appID)
}

// Post mirrors one notification to a peer.
//
// The filter runs first, before anything is marshalled: an excluded
// notification must not exist on the wire even in a buffer that is then
// discarded (PRD R22).
func (c *Capability) Post(ctx context.Context, sess session.Session, n Notification) error {
	if n.Key == "" {
		return ErrNoKey
	}
	if !c.Allowed(n.AppID) {
		return fmt.Errorf("%w: %s", ErrFiltered, n.AppID)
	}
	if err := c.validate(n); err != nil {
		return err
	}

	msg := &openairv1.Posted{
		Key:      n.Key,
		AppId:    n.AppID,
		AppName:  n.AppName,
		Title:    n.Title,
		Body:     n.Body,
		IconPng:  n.IconPNG,
		Category: n.Category,
	}
	if !n.PostedAt.IsZero() {
		msg.PostedAt = n.PostedAt.UnixMilli()
	} else {
		msg.PostedAt = time.Now().UnixMilli()
	}
	for _, a := range n.Actions {
		msg.Actions = append(msg.Actions, &openairv1.NotificationAction{
			ActionId:    a.ID,
			Label:       a.Label,
			AcceptsText: a.AcceptsText,
		})
	}

	if err := sess.Send(ctx, CapID, MsgPosted, msg); err != nil {
		return err
	}
	c.remember(n.Key, sess.Peer().DeviceID)
	return nil
}

// Remove tells a peer a notification is gone.
func (c *Capability) Remove(ctx context.Context, sess session.Session, key string) error {
	if key == "" {
		return ErrNoKey
	}
	c.forget(key, sess.Peer().DeviceID)
	return sess.Send(ctx, CapID, MsgRemoved, &openairv1.Removed{Key: key})
}

// Dismiss is the sink telling the source a person cleared the notification.
// The source is what makes it disappear from the other sinks too (§12).
func (c *Capability) Dismiss(ctx context.Context, sess session.Session, key string) error {
	if key == "" {
		return ErrNoKey
	}
	return sess.Send(ctx, CapID, MsgDismiss, &openairv1.Dismiss{Key: key})
}

// InvokeAction presses a notification's button on the source, optionally with
// text for an inline reply.
func (c *Capability) InvokeAction(ctx context.Context, sess session.Session, key, actionID, text string) error {
	if key == "" || actionID == "" {
		return errors.New("notifications: an action needs a key and an action id")
	}
	if len(text) > c.maxText() {
		return fmt.Errorf("%w: reply text is %d bytes", ErrTooLarge, len(text))
	}
	return sess.Send(ctx, CapID, MsgActionInvoke, &openairv1.ActionInvoke{
		Key:      key,
		ActionId: actionID,
		Text:     text,
	})
}

// Sinks reports which peers were told about a key, so that a source handling a
// dismiss can tell the others. Empty for a key this device never posted.
func (c *Capability) Sinks(key string) []identity.DeviceID {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]identity.DeviceID, 0, len(c.posted[key]))
	for id := range c.posted[key] {
		out = append(out, id)
	}
	return out
}

func (c *Capability) remember(key string, peer identity.DeviceID) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.posted[key] == nil {
		c.posted[key] = map[identity.DeviceID]struct{}{}
	}
	c.posted[key][peer] = struct{}{}
}

func (c *Capability) forget(key string, peer identity.DeviceID) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if set := c.posted[key]; set != nil {
		delete(set, peer)
		if len(set) == 0 {
			delete(c.posted, key)
		}
	}
}

// Forget drops all record of a key, for a source that has cleared it entirely.
func (c *Capability) Forget(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.posted, key)
}

// Serve handles an inbound message. All four of §12's messages travel on the
// control stream: a notification is small and interactive, and giving each one
// a stream would cost a round trip for something that is already one message.
func (c *Capability) Serve(ctx context.Context, sess session.Session, msgType uint16, payload []byte) error {
	switch msgType {
	case MsgPosted:
		return c.onPosted(ctx, sess, payload)
	case MsgRemoved:
		return c.onRemoved(ctx, sess, payload)
	case MsgDismiss:
		return c.onDismiss(ctx, sess, payload)
	case MsgActionInvoke:
		return c.onAction(ctx, sess, payload)
	default:
		// §3.1: unknown message types are ignored, never fatal.
		return session.ErrUnknownMsgType
	}
}

// ServeStream refuses: §12 opens no streams.
func (c *Capability) ServeStream(_ context.Context, _ session.Session, st session.Stream, _ uint16, _ []byte) error {
	st.Reset(uint32(session.CodeProtocolViolation))
	return errors.New("notifications: §12 travels on the control stream, not a capability stream")
}

func (c *Capability) onPosted(ctx context.Context, sess session.Session, payload []byte) error {
	var m openairv1.Posted
	if err := proto.Unmarshal(payload, &m); err != nil {
		return fmt.Errorf("notifications: malformed Posted: %w", err)
	}
	if m.GetKey() == "" {
		return ErrNoKey
	}

	n := Notification{
		Key:      m.GetKey(),
		AppID:    m.GetAppId(),
		AppName:  m.GetAppName(),
		Title:    m.GetTitle(),
		Body:     m.GetBody(),
		IconPNG:  m.GetIconPng(),
		Category: m.GetCategory(),
	}
	if ms := m.GetPostedAt(); ms > 0 {
		n.PostedAt = time.UnixMilli(ms)
	}
	for _, a := range m.GetActions() {
		n.Actions = append(n.Actions, Action{
			ID:          a.GetActionId(),
			Label:       a.GetLabel(),
			AcceptsText: a.GetAcceptsText(),
		})
	}
	// The caps are enforced on what arrives as well as on what is sent. A peer
	// is not obliged to have been built from this source tree.
	if err := c.validate(n); err != nil {
		return err
	}
	if c.cfg.OnPosted == nil {
		return nil
	}
	c.cfg.OnPosted(ctx, sess.Peer(), n)
	return nil
}

func (c *Capability) onRemoved(ctx context.Context, sess session.Session, payload []byte) error {
	var m openairv1.Removed
	if err := proto.Unmarshal(payload, &m); err != nil {
		return fmt.Errorf("notifications: malformed Removed: %w", err)
	}
	// §12: a sink must tolerate a Removed for a key it never saw. That happens
	// routinely -- a notification filtered here, a sink that connected after it
	// was posted -- so it is not an error and not worth a log line.
	if c.cfg.OnRemoved == nil {
		return nil
	}
	c.cfg.OnRemoved(ctx, sess.Peer(), m.GetKey())
	return nil
}

func (c *Capability) onDismiss(ctx context.Context, sess session.Session, payload []byte) error {
	var m openairv1.Dismiss
	if err := proto.Unmarshal(payload, &m); err != nil {
		return fmt.Errorf("notifications: malformed Dismiss: %w", err)
	}
	if m.GetKey() == "" {
		return ErrNoKey
	}
	if c.cfg.OnDismiss == nil {
		return nil
	}
	c.cfg.OnDismiss(ctx, sess.Peer(), m.GetKey())
	return nil
}

func (c *Capability) onAction(ctx context.Context, sess session.Session, payload []byte) error {
	var m openairv1.ActionInvoke
	if err := proto.Unmarshal(payload, &m); err != nil {
		return fmt.Errorf("notifications: malformed ActionInvoke: %w", err)
	}
	if m.GetKey() == "" || m.GetActionId() == "" {
		return errors.New("notifications: ActionInvoke needs a key and an action id")
	}
	if len(m.GetText()) > c.maxText() {
		return fmt.Errorf("%w: reply text is %d bytes", ErrTooLarge, len(m.GetText()))
	}
	if c.cfg.OnAction == nil {
		return nil
	}
	c.cfg.OnAction(ctx, sess.Peer(), m.GetKey(), m.GetActionId(), m.GetText())
	return nil
}

// validate applies the caps §12 leaves to the implementation.
func (c *Capability) validate(n Notification) error {
	switch {
	case len(n.IconPNG) > c.maxIcon():
		return fmt.Errorf("%w: icon is %d bytes, cap is %d", ErrTooLarge, len(n.IconPNG), c.maxIcon())
	case len(n.Title) > c.maxText():
		return fmt.Errorf("%w: title is %d bytes, cap is %d", ErrTooLarge, len(n.Title), c.maxText())
	case len(n.Body) > c.maxText():
		return fmt.Errorf("%w: body is %d bytes, cap is %d", ErrTooLarge, len(n.Body), c.maxText())
	case len(n.Actions) > c.maxActions():
		return fmt.Errorf("%w: %d actions, cap is %d", ErrTooLarge, len(n.Actions), c.maxActions())
	}
	return nil
}

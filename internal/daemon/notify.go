package daemon

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/shreyashsri79/openair/internal/caps/notifications"
	"github.com/shreyashsri79/openair/internal/identity"
	"github.com/shreyashsri79/openair/internal/ipc"
	"github.com/shreyashsri79/openair/internal/session"
	openairv1 "github.com/shreyashsri79/openair/internal/wire/openair/v1"
)

// Notifications, M12 (§12). A notification posted on one device appears on the
// others, and clearing it anywhere clears it everywhere.
//
// The daemon is both halves at once, because §12's messages are symmetric and
// desktop-to-desktop forwarding (PRD R23) is the phone case with a different
// sender. What it adds over the capability is fan-out: a source posts to every
// device it has a session with, and a dismiss arriving from one of them is
// forwarded to the rest.

// notifyTimeout bounds one fan-out. A notification is small and interactive;
// waiting a long time to deliver one is worse than not delivering it.
const notifyTimeout = 15 * time.Second

// onNotify posts a notification from a local shell to peers.
func (d *Daemon) onNotify(ctx context.Context, c *client, payload []byte) {
	var req openairv1.DaemonNotifyRequest
	if !unmarshal(c, payload, &req) {
		return
	}
	reply := &openairv1.DaemonNotifyResponse{RequestId: req.GetRequestId()}

	n := notificationFromWire(req.GetNotification())
	switch {
	case n.Key == "":
		reply.Error = notifications.ErrNoKey.Error()
	case !d.notes.Allowed(n.AppID):
		// The filter is local policy and it runs here, before anything is
		// marshalled: an excluded notification must not exist on the wire even
		// briefly (PRD R22, D-76).
		reply.Filtered = true
		d.cfg.Logf("notification from %s withheld by this device's filter", n.AppID)
	default:
		delivered, err := d.postNotification(ctx, req.GetDevice(), n)
		reply.Delivered = uint32(delivered)
		if err != nil {
			reply.Error = err.Error()
		}
	}
	_ = c.peer.Send(ipc.MsgNotifyResponse, reply)
}

// postNotification sends to one device, or to every device with a live session.
func (d *Daemon) postNotification(ctx context.Context, target string, n notifications.Notification) (int, error) {
	ctx, cancel := context.WithTimeout(ctx, notifyTimeout)
	defer cancel()

	if target != "" {
		sess, err := d.sessionTo(ctx, target)
		if err != nil {
			return 0, err
		}
		if err := d.notes.Post(ctx, sess, n); err != nil {
			return 0, err
		}
		return 1, nil
	}

	// No target means every device already connected. Deliberately not every
	// *paired* device: dialling half a dozen machines because a build finished
	// would turn a notification into a connection storm, and a device that is
	// not connected is not somewhere a person is looking.
	sessions := d.liveSessions()
	if len(sessions) == 0 {
		return 0, errors.New("no device is connected to notify")
	}
	var (
		delivered int
		lastErr   error
	)
	for _, sess := range sessions {
		if err := d.notes.Post(ctx, sess, n); err != nil {
			lastErr = err
			continue
		}
		delivered++
	}
	if delivered == 0 {
		return 0, lastErr
	}
	return delivered, nil
}

// onDismissRequest clears a notification from a shell: on a sink that means
// telling the source, on a source it means telling the other sinks.
func (d *Daemon) onDismissRequest(ctx context.Context, c *client, payload []byte) {
	var req openairv1.DaemonDismissRequest
	if !unmarshal(c, payload, &req) {
		return
	}
	reply := &openairv1.DaemonDismissResponse{RequestId: req.GetRequestId()}

	if err := d.dismiss(ctx, req.GetDevice(), req.GetKey(), req.GetActionId(), req.GetText()); err != nil {
		reply.Error = err.Error()
	}
	_ = c.peer.Send(ipc.MsgDismissResponse, reply)
}

func (d *Daemon) dismiss(ctx context.Context, target, key, actionID, text string) error {
	if key == "" {
		return notifications.ErrNoKey
	}
	ctx, cancel := context.WithTimeout(ctx, notifyTimeout)
	defer cancel()

	if target != "" {
		sess, err := d.sessionTo(ctx, target)
		if err != nil {
			return err
		}
		if actionID != "" {
			return d.notes.InvokeAction(ctx, sess, key, actionID, text)
		}
		return d.notes.Dismiss(ctx, sess, key)
	}

	// No target: this is a notification this device posted, so clearing it
	// means telling every sink that has it.
	sinks := d.notes.Sinks(key)
	if len(sinks) == 0 {
		return fmt.Errorf("no device was told about %q", key)
	}
	var lastErr error
	for _, id := range sinks {
		sess, live := d.sessionFor(id)
		if !live {
			continue
		}
		if err := d.notes.Remove(ctx, sess, key); err != nil {
			lastErr = err
		}
	}
	d.notes.Forget(key)
	d.broadcast(&openairv1.DaemonEvent{
		Kind: openairv1.DaemonEventKind_DAEMON_EVENT_KIND_NOTIFICATION_REMOVED,
		Text: key,
	})
	return lastErr
}

// liveSessions is every session currently open, for a fan-out.
func (d *Daemon) liveSessions() []session.Session {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]session.Session, 0, len(d.sessions))
	for _, s := range d.sessions {
		out = append(out, s)
	}
	return out
}

// onNotificationPosted is the sink side: show it to whoever is watching.
func (d *Daemon) onNotificationPosted(_ context.Context, peer identity.Peer, n notifications.Notification) {
	d.cfg.Logf("notification from %s: %s — %s", peer.DisplayName, n.AppName, n.Title)
	d.broadcast(&openairv1.DaemonEvent{
		Kind:         openairv1.DaemonEventKind_DAEMON_EVENT_KIND_NOTIFICATION,
		DeviceId:     string(peer.DeviceID),
		Text:         n.Title,
		Notification: notificationToWire(n),
		Ok:           true,
	})
}

// onNotificationRemoved is the source saying a notification is gone. §12
// requires tolerating a key this device never saw, which is what this does by
// simply forwarding it.
func (d *Daemon) onNotificationRemoved(_ context.Context, peer identity.Peer, key string) {
	d.broadcast(&openairv1.DaemonEvent{
		Kind:     openairv1.DaemonEventKind_DAEMON_EVENT_KIND_NOTIFICATION_REMOVED,
		DeviceId: string(peer.DeviceID),
		Text:     key,
	})
}

// onNotificationDismissed is the source side of §12's
// dismiss-on-one-dismisses-everywhere: one sink cleared it, so the others are
// told, and so is anything watching here.
func (d *Daemon) onNotificationDismissed(ctx context.Context, peer identity.Peer, key string) {
	d.cfg.Logf("%s dismissed notification %s", peer.DeviceID.Fingerprint(), key)

	sinks := d.notes.Sinks(key)
	d.notes.Forget(key)
	d.broadcast(&openairv1.DaemonEvent{
		Kind:     openairv1.DaemonEventKind_DAEMON_EVENT_KIND_NOTIFICATION_REMOVED,
		DeviceId: string(peer.DeviceID),
		Text:     key,
	})

	// Off this goroutine: it is the peer's capability queue (D-41), and a slow
	// sink must not hold up the next message from the device that dismissed.
	go func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), notifyTimeout)
		defer cancel()
		for _, id := range sinks {
			if id == peer.DeviceID {
				continue
			}
			sess, live := d.sessionFor(id)
			if !live {
				continue
			}
			if err := d.notes.Remove(ctx, sess, key); err != nil {
				d.cfg.Logf("clearing %s on %s: %v", key, id.Fingerprint(), err)
			}
		}
	}()
}

// onNotificationAction is a sink pressing a button. The daemon has nothing to
// press -- the application that posted it is not this process -- so it reports
// the invocation to whatever is watching and lets that decide.
func (d *Daemon) onNotificationAction(_ context.Context, peer identity.Peer, key, actionID, text string) {
	d.cfg.Logf("%s invoked %s on notification %s", peer.DeviceID.Fingerprint(), actionID, key)
	d.broadcast(&openairv1.DaemonEvent{
		Kind:     openairv1.DaemonEventKind_DAEMON_EVENT_KIND_NOTIFICATION,
		DeviceId: string(peer.DeviceID),
		Text:     fmt.Sprintf("%s: %s", actionID, text),
		Ok:       false,
	})
}

func notificationFromWire(m *openairv1.Posted) notifications.Notification {
	if m == nil {
		return notifications.Notification{}
	}
	n := notifications.Notification{
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
		n.Actions = append(n.Actions, notifications.Action{
			ID:          a.GetActionId(),
			Label:       a.GetLabel(),
			AcceptsText: a.GetAcceptsText(),
		})
	}
	return n
}

func notificationToWire(n notifications.Notification) *openairv1.Posted {
	m := &openairv1.Posted{
		Key:      n.Key,
		AppId:    n.AppID,
		AppName:  n.AppName,
		Title:    n.Title,
		Body:     n.Body,
		IconPng:  n.IconPNG,
		Category: n.Category,
	}
	if !n.PostedAt.IsZero() {
		m.PostedAt = n.PostedAt.UnixMilli()
	}
	for _, a := range n.Actions {
		m.Actions = append(m.Actions, &openairv1.NotificationAction{
			ActionId:    a.ID,
			Label:       a.Label,
			AcceptsText: a.AcceptsText,
		})
	}
	return m
}

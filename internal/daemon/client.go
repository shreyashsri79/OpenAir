package daemon

import (
	"context"
	"errors"
	"fmt"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/shreyashsri79/openair/internal/ipc"
	openairv1 "github.com/shreyashsri79/openair/internal/wire/openair/v1"
)

// Client is a shell's end of the IPC link: the CLI today, a tray UI later.
//
// It is deliberately thin. Every method is one request and one reply, and the
// interesting asymmetry -- the daemon asking the client a question -- is the
// PromptFunc handed to Connect rather than anything the caller has to poll for.
type Client struct {
	peer   *ipc.Peer
	cancel context.CancelFunc
}

// EventFunc receives unsolicited events once Subscribe has been called.
type EventFunc func(*openairv1.DaemonEvent)

// PromptFunc answers a question from the daemon. Returning false refuses.
//
// A client that passes nil here can still subscribe to events but must not ask
// for prompts: the daemon takes "no answer" as a refusal, and a client that
// silently drops a SAS prompt would look like a device that cannot pair.
type PromptFunc func(*openairv1.DaemonPrompt) bool

// ErrNoDaemon is what Connect returns when nothing is listening. Callers use
// it to decide whether to fall back to driving a session directly.
var ErrNoDaemon = errors.New("no daemon is listening")

// Connect opens the IPC connection and starts its read loop.
func Connect(ctx context.Context, socketPath string, onEvent EventFunc, onPrompt PromptFunc) (*Client, error) {
	if socketPath == "" {
		socketPath = ipc.DefaultSocketPath()
	}
	nc, err := ipc.Dial(socketPath)
	if err != nil {
		return nil, fmt.Errorf("%w at %s: %v", ErrNoDaemon, socketPath, err)
	}

	runCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	c := &Client{cancel: cancel}
	c.peer = ipc.NewPeer(nc, func(ctx context.Context, p *ipc.Peer, msgType uint16, payload []byte) {
		switch msgType {
		case ipc.MsgEvent:
			if onEvent == nil {
				return
			}
			var ev openairv1.DaemonEvent
			if err := proto.Unmarshal(payload, &ev); err == nil {
				onEvent(&ev)
			}
		case ipc.MsgPrompt:
			var pr openairv1.DaemonPrompt
			if err := proto.Unmarshal(payload, &pr); err != nil {
				return
			}
			approve := false
			if onPrompt != nil {
				approve = onPrompt(&pr)
			}
			_ = p.Send(ipc.MsgPromptResponse, &openairv1.DaemonPromptResponse{
				RequestId: pr.GetRequestId(),
				Approve:   approve,
			})
		}
	})
	go c.peer.Serve(runCtx)
	return c, nil
}

// Close ends the connection. Pending calls fail rather than hang.
func (c *Client) Close() error {
	c.cancel()
	return c.peer.Close()
}

// Done is closed when the daemon goes away.
func (c *Client) Done() <-chan struct{} { return c.peer.Done() }

func (c *Client) Status(ctx context.Context) (*openairv1.DaemonStatusResponse, error) {
	var resp openairv1.DaemonStatusResponse
	if err := c.peer.Do(ctx, ipc.MsgStatusRequest, &openairv1.DaemonStatusRequest{}, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

func (c *Client) Devices(ctx context.Context, pairedOnly bool) ([]*openairv1.DaemonDevice, error) {
	var resp openairv1.DaemonDeviceListResponse
	req := &openairv1.DaemonDeviceListRequest{PairedOnly: pairedOnly}
	if err := c.peer.Do(ctx, ipc.MsgDeviceListRequest, req, &resp); err != nil {
		return nil, err
	}
	return resp.GetDevices(), nil
}

// Send blocks until the transfer finishes, because its result is the only
// thing that says whether the bytes arrived intact.
func (c *Client) Send(ctx context.Context, target string, paths []string) (*openairv1.DaemonSendResponse, error) {
	var resp openairv1.DaemonSendResponse
	req := &openairv1.DaemonSendRequest{Target: target, Paths: paths}
	if err := c.peer.Do(ctx, ipc.MsgSendRequest, req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// Pair runs §5 through the daemon. An empty offer displays this device's own
// and waits; the displayed code arrives as an event before the reply does.
func (c *Client) Pair(ctx context.Context, offer string, timeout time.Duration) (*openairv1.DaemonPairResponse, error) {
	var resp openairv1.DaemonPairResponse
	req := &openairv1.DaemonPairRequest{Offer: offer, TimeoutMs: timeout.Milliseconds()}
	if err := c.peer.Do(ctx, ipc.MsgPairRequest, req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// Subscribe turns this connection into an event sink. prompts additionally
// makes it one the daemon may ask questions of, which is what lets a transfer
// be accepted by a human rather than by policy.
func (c *Client) Subscribe(ctx context.Context, prompts bool) error {
	var resp openairv1.DaemonSubscribeResponse
	req := &openairv1.DaemonSubscribeRequest{Prompts: prompts}
	return c.peer.Do(ctx, ipc.MsgSubscribeRequest, req, &resp)
}

// Clipboard pushes clipboard content to a device (§9, M5).
func (c *Client) Clipboard(ctx context.Context, target string, push *openairv1.ClipboardPush) error {
	var resp openairv1.DaemonClipboardResponse
	req := &openairv1.DaemonClipboardRequest{Target: target, Push: push}
	return c.peer.Do(ctx, ipc.MsgClipboardRequest, req, &resp)
}

// Unlock starts an unlock session for one peer (D-18, D-30). The credential
// travels over the local socket; see the note in the daemon's unlock.go for why
// the daemon does not prompt for it itself.
func (c *Client) Unlock(ctx context.Context, deviceID string, passphrase, keystoreKEK []byte, neverExpire bool, lifetime time.Duration) (*openairv1.DaemonUnlockResponse, error) {
	var resp openairv1.DaemonUnlockResponse
	req := &openairv1.DaemonUnlockRequest{
		DeviceId:    deviceID,
		Passphrase:  passphrase,
		KeystoreKek: keystoreKEK,
		NeverExpire: neverExpire,
		LifetimeMs:  lifetime.Milliseconds(),
	}
	if err := c.peer.Do(ctx, ipc.MsgUnlockRequest, req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// Lock ends one peer's unlock session, or every session when deviceID is empty.
func (c *Client) Lock(ctx context.Context, deviceID string) error {
	var resp openairv1.DaemonLockResponse
	return c.peer.Do(ctx, ipc.MsgLockRequest, &openairv1.DaemonLockRequest{DeviceId: deviceID}, &resp)
}

// Trust promotes or demotes a paired device (§6.4). An empty authPolicy leaves
// the existing one alone.
func (c *Client) Trust(ctx context.Context, deviceID string, level openairv1.TrustLevel, authPolicy string) (openairv1.TrustLevel, error) {
	var resp openairv1.DaemonTrustResponse
	req := &openairv1.DaemonTrustRequest{DeviceId: deviceID, Level: level, AuthPolicy: authPolicy}
	if err := c.peer.Do(ctx, ipc.MsgTrustRequest, req, &resp); err != nil {
		return openairv1.TrustLevel_TRUST_LEVEL_UNSPECIFIED, err
	}
	return resp.GetLevel(), nil
}

// Browse lists what a paired device shares, or what is inside one of its
// shares (M10, §11). An empty path asks for the share list itself.
//
// truncated says more entries remain: ask again at offset+len(entries). §11.1
// pages listings so a directory of 100k entries is many small answers rather
// than one envelope nobody can send.
func (c *Client) Browse(ctx context.Context, device, path string, offset, limit int) (entries []*openairv1.FileStat, truncated bool, err error) {
	var resp openairv1.DaemonBrowseResponse
	req := &openairv1.DaemonBrowseRequest{
		Device: device,
		Path:   path,
		Offset: uint32(offset),
		Limit:  uint32(limit),
	}
	if err := c.peer.Do(ctx, ipc.MsgBrowseRequest, req, &resp); err != nil {
		return nil, false, err
	}
	if resp.GetError() != "" {
		return nil, false, errors.New(resp.GetError())
	}
	return resp.GetEntries(), resp.GetTruncated(), nil
}

// Fetch copies a remote file, or a range of one, to a local path.
//
// length 0 means "to the end". The daemon does the range reads and the writing:
// the shell names a destination and hears how many bytes landed.
func (c *Client) Fetch(ctx context.Context, device, path, dest string, offset, length uint64) (uint64, error) {
	var resp openairv1.DaemonFetchResponse
	req := &openairv1.DaemonFetchRequest{
		Device: device,
		Path:   path,
		Dest:   dest,
		Offset: offset,
		Length: length,
	}
	if err := c.peer.Do(ctx, ipc.MsgFetchRequest, req, &resp); err != nil {
		return 0, err
	}
	if resp.GetError() != "" {
		return resp.GetBytesWritten(), errors.New(resp.GetError())
	}
	return resp.GetBytesWritten(), nil
}

// Stream publishes a remote file on a loopback HTTP URL that a media player can
// open (M11, §11.2). Every Range request the player makes becomes a range read
// on the wire; nothing is downloaded ahead of what it asks for beyond the
// read-ahead window.
func (c *Client) Stream(ctx context.Context, device, path string) (url, mime string, size uint64, err error) {
	var resp openairv1.DaemonStreamResponse
	req := &openairv1.DaemonStreamRequest{Device: device, Path: path}
	if err := c.peer.Do(ctx, ipc.MsgStreamRequest, req, &resp); err != nil {
		return "", "", 0, err
	}
	if resp.GetError() != "" {
		return "", "", 0, errors.New(resp.GetError())
	}
	return resp.GetUrl(), resp.GetMime(), resp.GetSize(), nil
}

// StopStream withdraws a URL published by Stream.
func (c *Client) StopStream(ctx context.Context, device, path string) error {
	var resp openairv1.DaemonStreamResponse
	req := &openairv1.DaemonStreamRequest{Device: device, Path: path, Stop: true}
	if err := c.peer.Do(ctx, ipc.MsgStreamRequest, req, &resp); err != nil {
		return err
	}
	if resp.GetError() != "" {
		return errors.New(resp.GetError())
	}
	return nil
}

// Input drives another device's keyboard and pointer (M14, §13).
//
// The daemon holds the §6.3 session that authorises it and signs the Owned
// proof, so a shell only says what it wants to happen.
func (c *Client) Input(ctx context.Context, device string, actions []*openairv1.InputAction) (int, error) {
	var resp openairv1.DaemonInputResponse
	req := &openairv1.DaemonInputRequest{Device: device, Actions: actions}
	if err := c.peer.Do(ctx, ipc.MsgInputRequest, req, &resp); err != nil {
		return 0, err
	}
	if resp.GetError() != "" {
		return int(resp.GetSent()), errors.New(resp.GetError())
	}
	return int(resp.GetSent()), nil
}

// StopInput ends the control session with a device (§6.3's SessionEnd), which
// is what lowers the indicator on it.
func (c *Client) StopInput(ctx context.Context, device string) error {
	var resp openairv1.DaemonInputResponse
	req := &openairv1.DaemonInputRequest{Device: device, Stop: true}
	if err := c.peer.Do(ctx, ipc.MsgInputRequest, req, &resp); err != nil {
		return err
	}
	if resp.GetError() != "" {
		return errors.New(resp.GetError())
	}
	return nil
}

// Mirror watches another device's screen and returns the loopback URL the
// frames are published at (M15, §14).
func (c *Client) Mirror(ctx context.Context, device string, width, height, fps, bitrate int) (string, error) {
	var resp openairv1.DaemonMirrorResponse
	req := &openairv1.DaemonMirrorRequest{
		Device:  device,
		Width:   uint32(width),
		Height:  uint32(height),
		Fps:     uint32(fps),
		Bitrate: uint32(bitrate),
	}
	if err := c.peer.Do(ctx, ipc.MsgMirrorRequest, req, &resp); err != nil {
		return "", err
	}
	if resp.GetError() != "" {
		return "", errors.New(resp.GetError())
	}
	return resp.GetUrl(), nil
}

// StopMirror ends a mirror session, which lowers the indicator on the device
// whose screen it was.
func (c *Client) StopMirror(ctx context.Context, device string) error {
	var resp openairv1.DaemonMirrorResponse
	req := &openairv1.DaemonMirrorRequest{Device: device, Stop: true}
	if err := c.peer.Do(ctx, ipc.MsgMirrorRequest, req, &resp); err != nil {
		return err
	}
	if resp.GetError() != "" {
		return errors.New(resp.GetError())
	}
	return nil
}

// Notify mirrors a notification to a device, or to every connected device when
// device is empty (M12, §12).
//
// The source-side filter runs in the daemon, so a notification this device's
// policy excludes never reaches the wire — `filtered` reports that, and it is
// not an error (PRD R22).
func (c *Client) Notify(ctx context.Context, device string, n *openairv1.Posted) (delivered int, filtered bool, err error) {
	var resp openairv1.DaemonNotifyResponse
	req := &openairv1.DaemonNotifyRequest{Device: device, Notification: n}
	if err := c.peer.Do(ctx, ipc.MsgNotifyRequest, req, &resp); err != nil {
		return 0, false, err
	}
	if resp.GetError() != "" {
		return int(resp.GetDelivered()), resp.GetFiltered(), errors.New(resp.GetError())
	}
	return int(resp.GetDelivered()), resp.GetFiltered(), nil
}

// Dismiss clears a notification. With a device, it tells that device — which on
// a sink means telling the source. With no device, it clears one this machine
// posted, telling every sink that has it (§12).
func (c *Client) Dismiss(ctx context.Context, device, key string) error {
	var resp openairv1.DaemonDismissResponse
	req := &openairv1.DaemonDismissRequest{Device: device, Key: key}
	if err := c.peer.Do(ctx, ipc.MsgDismissRequest, req, &resp); err != nil {
		return err
	}
	if resp.GetError() != "" {
		return errors.New(resp.GetError())
	}
	return nil
}

// InvokeAction presses a button on a mirrored notification, with text for an
// inline reply (§12, PRD R22).
func (c *Client) InvokeAction(ctx context.Context, device, key, actionID, text string) error {
	var resp openairv1.DaemonDismissResponse
	req := &openairv1.DaemonDismissRequest{Device: device, Key: key, ActionId: actionID, Text: text}
	if err := c.peer.Do(ctx, ipc.MsgDismissRequest, req, &resp); err != nil {
		return err
	}
	if resp.GetError() != "" {
		return errors.New(resp.GetError())
	}
	return nil
}

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

package daemon

import (
	"context"
	"errors"
	"fmt"
	"net"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/shreyashsri79/openair/internal/caps/files"
	"github.com/shreyashsri79/openair/internal/identity"
	"github.com/shreyashsri79/openair/internal/ipc"
	"github.com/shreyashsri79/openair/internal/pairing"
	"github.com/shreyashsri79/openair/internal/session"
	openairv1 "github.com/shreyashsri79/openair/internal/wire/openair/v1"
)

// acceptClients serves the local socket. Every connection is one shell.
func (d *Daemon) acceptClients(ctx context.Context) {
	for {
		c, err := d.ipcLn.Accept()
		if err != nil {
			if ctx.Err() == nil {
				select {
				case <-ctx.Done():
				default:
					d.cfg.Logf("ipc accept: %v", err)
				}
			}
			return
		}

		// The local trust boundary (D-29). Socket permissions are the first
		// control and this is the second; a connection that fails it never
		// reaches a handler.
		if err := ipc.CheckPeer(c); err != nil {
			d.cfg.Logf("ipc: %v", err)
			c.Close()
			continue
		}

		go d.serveClient(ctx, c)
	}
}

// serveClient runs one IPC connection to its end.
//
// A client that disconnects, cleanly or not, takes nothing with it: the daemon
// keeps its sessions, its trust store and its listener. That is the point of
// there being a daemon.
func (d *Daemon) serveClient(ctx context.Context, nc net.Conn) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var c *client
	peer := ipc.NewPeer(nc, func(ctx context.Context, p *ipc.Peer, msgType uint16, payload []byte) {
		d.handle(ctx, c, msgType, payload)
	})
	c = newClient(peer)

	d.addClient(c)
	defer d.removeClient(c)

	go c.pump(ctx)
	if err := peer.Serve(ctx); err != nil {
		d.cfg.Logf("ipc client: %v", err)
	}
}

// handle dispatches one inbound IPC message.
//
// An unknown message type is ignored rather than fatal, which is §3.1 applied
// to the local link: a newer CLI talking to an older daemon should lose the
// feature it asked for, not the connection.
func (d *Daemon) handle(ctx context.Context, c *client, msgType uint16, payload []byte) {
	switch msgType {
	case ipc.MsgStatusRequest:
		d.onStatus(c, payload)
	case ipc.MsgDeviceListRequest:
		d.onDeviceList(c, payload)
	case ipc.MsgSubscribeRequest:
		d.onSubscribe(c, payload)
	case ipc.MsgSendRequest:
		d.onSend(ctx, c, payload)
	case ipc.MsgPairRequest:
		d.onPair(ctx, c, payload)
	case ipc.MsgClipboardRequest:
		d.onClipboard(ctx, c, payload)
	default:
		d.cfg.Logf("ipc: ignoring unknown message type %d", msgType)
	}
}

// unmarshal decodes a request, answering the client with an error if it cannot.
func unmarshal[T proto.Message](c *client, payload []byte, msg T) bool {
	if err := proto.Unmarshal(payload, msg); err != nil {
		_ = c.peer.ReplyError(0, session.CodeProtocolViolation, "malformed request: %v", err)
		return false
	}
	return true
}

func (d *Daemon) onStatus(c *client, payload []byte) {
	var req openairv1.DaemonStatusRequest
	if !unmarshal(c, payload, &req) {
		return
	}

	peers := d.store.List()

	d.mu.Lock()
	sessions := len(d.sessions)
	subs := 0
	for cl := range d.clients {
		if cl.canPrompt() {
			subs++
		}
	}
	d.mu.Unlock()

	_ = c.peer.Reply(ipc.MsgStatusResponse, req.GetRequestId(), &openairv1.DaemonStatusResponse{
		DeviceId:       string(d.id.DeviceID()),
		DisplayName:    d.cfg.DisplayName,
		Platform:       platform(),
		ListenAddr:     d.ln.Addr(),
		DestDir:        d.cfg.DestDir,
		StartedUnix:    d.started.Unix(),
		PairedDevices:  uint32(len(peers)),
		ActiveSessions: uint32(sessions),
		Announcing:     d.disco != nil && !d.cfg.NoAnnounce,
		AutoAccept:     d.cfg.AutoAccept,
		Subscribers:    uint32(subs),
	})
}

// onDeviceList merges two sources that answer different questions: the trust
// store says who this device may talk to, discovery says who is reachable right
// now. A device list showing only one of them is missing half the answer.
func (d *Daemon) onDeviceList(c *client, payload []byte) {
	var req openairv1.DaemonDeviceListRequest
	if !unmarshal(c, payload, &req) {
		return
	}

	paired := d.store.List()

	byID := map[identity.DeviceID]*openairv1.DaemonDevice{}
	out := make([]*openairv1.DaemonDevice, 0, len(paired))
	for _, p := range paired {
		_, open := d.sessionFor(p.DeviceID)
		dev := &openairv1.DaemonDevice{
			DeviceId:    string(p.DeviceID),
			DisplayName: p.DisplayName,
			Platform:    p.Platform,
			Paired:      true,
			Level:       trustLevelToWire(p.Level),
			SessionOpen: open,
		}
		byID[p.DeviceID] = dev
		out = append(out, dev)
	}

	if d.disco != nil {
		for _, cand := range d.disco.Peers() {
			if dev, ok := byID[cand.DeviceID]; ok {
				dev.Addrs = cand.Addrs
				if dev.DisplayName == "" {
					dev.DisplayName = cand.DisplayName
				}
				continue
			}
			if req.GetPairedOnly() {
				continue
			}
			out = append(out, &openairv1.DaemonDevice{
				DeviceId:    string(cand.DeviceID),
				DisplayName: cand.DisplayName,
				Addrs:       cand.Addrs,
			})
		}
	}

	_ = c.peer.Reply(ipc.MsgDeviceListResponse, req.GetRequestId(),
		&openairv1.DaemonDeviceListResponse{Devices: out})
}

func (d *Daemon) onSubscribe(c *client, payload []byte) {
	var req openairv1.DaemonSubscribeRequest
	if !unmarshal(c, payload, &req) {
		return
	}
	c.subscribe(req.GetPrompts())
	_ = c.peer.Reply(ipc.MsgSubscribeResponse, req.GetRequestId(), &openairv1.DaemonSubscribeResponse{})
}

// onSend offers local files to a device and answers when the transfer is done.
//
// It answers late on purpose: a reply sent at the start would make `openair
// send` return before the receiver had verified a single digest, and the one
// thing the caller needs to know is whether the bytes arrived intact.
func (d *Daemon) onSend(ctx context.Context, c *client, payload []byte) {
	var req openairv1.DaemonSendRequest
	if !unmarshal(c, payload, &req) {
		return
	}
	if len(req.GetPaths()) == 0 {
		_ = c.peer.ReplyError(req.GetRequestId(), 0, "no files given")
		return
	}

	transferID, peerID, err := d.send(ctx, req.GetTarget(), req.GetPaths())
	if err != nil {
		code := session.CodeNoError
		if pc, ok := session.ErrorCodeOf(err); ok {
			code = pc
		}
		_ = c.peer.ReplyError(req.GetRequestId(), code, "%v", err)
		return
	}
	_ = c.peer.Reply(ipc.MsgSendResponse, req.GetRequestId(), &openairv1.DaemonSendResponse{
		TransferId: transferID,
		DeviceId:   string(peerID),
	})
}

// onPair runs PROTOCOL.md §5 on the daemon's behalf.
//
// Both directions end in a prompt carrying the six digits, because §5.2 has no
// path that skips them. A client that subscribed without asking for prompts
// therefore cannot pair, which is correct: nothing would be showing the digits
// to a human.
func (d *Daemon) onPair(ctx context.Context, c *client, payload []byte) {
	var req openairv1.DaemonPairRequest
	if !unmarshal(c, payload, &req) {
		return
	}

	timeout := time.Duration(req.GetTimeoutMs()) * time.Millisecond
	if timeout <= 0 {
		timeout = pairPromptTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	peer, offer, err := d.pair(ctx, c, req.GetOffer())
	if err != nil {
		_ = c.peer.ReplyError(req.GetRequestId(), 0, "%v", err)
		return
	}
	_ = c.peer.Reply(ipc.MsgPairResponse, req.GetRequestId(), &openairv1.DaemonPairResponse{
		Offer:       offer,
		DeviceId:    string(peer.DeviceID),
		DisplayName: peer.DisplayName,
	})
}

// confirmPairing is the daemon's ConfirmFunc: it shows the SAS by asking a
// client, and refuses if nobody is there to look.
func (d *Daemon) confirmPairing(ctx context.Context, sas string, peer pairing.PeerInfo) (bool, error) {
	ok := d.ask(ctx, &openairv1.DaemonPrompt{
		Kind:        openairv1.DaemonPromptKind_DAEMON_PROMPT_KIND_PAIR_SAS,
		Text:        "do both devices show exactly these six digits?",
		DeviceId:    string(peer.DeviceID),
		DisplayName: peer.DisplayName,
		Platform:    peer.Platform,
		Sas:         sas,
	}, pairPromptTimeout)
	return ok, nil
}

// acceptTransfer is files.Config.Accept: ask a client, or apply policy.
func (d *Daemon) acceptTransfer(ctx context.Context, peer identity.Peer, offer files.Offer) (bool, error) {
	if d.cfg.AutoAccept {
		d.cfg.Logf("accepting %s from %s automatically", offer.TransferID, peer.DeviceID.Fingerprint())
		return true, nil
	}

	names := make([]string, 0, len(offer.Files))
	for _, f := range offer.Files {
		names = append(names, fmt.Sprintf("%s (%s)", f.GetPath(), humanBytes(f.GetSize())))
	}
	ok := d.ask(ctx, &openairv1.DaemonPrompt{
		Kind:        openairv1.DaemonPromptKind_DAEMON_PROMPT_KIND_ACCEPT_TRANSFER,
		Text:        fmt.Sprintf("accept %d file(s), %s?", len(offer.Files), humanBytes(offer.TotalBytes)),
		DeviceId:    string(peer.DeviceID),
		DisplayName: peer.DisplayName,
		Platform:    peer.Platform,
		Files:       names,
		TotalBytes:  offer.TotalBytes,
	}, d.cfg.PromptTimeout)

	if ok {
		d.broadcast(&openairv1.DaemonEvent{
			Kind:       openairv1.DaemonEventKind_DAEMON_EVENT_KIND_TRANSFER_STARTED,
			DeviceId:   string(peer.DeviceID),
			TransferId: offer.TransferID,
			BytesTotal: offer.TotalBytes,
		})
	}
	return ok, nil
}

func (d *Daemon) onTransferProgress(p files.Progress) {
	d.broadcast(&openairv1.DaemonEvent{
		Kind:       openairv1.DaemonEventKind_DAEMON_EVENT_KIND_TRANSFER_PROGRESS,
		TransferId: p.TransferID,
		BytesDone:  p.BytesReceived,
		BytesTotal: p.TotalBytes,
	})
}

func (d *Daemon) onTransferComplete(transferID string, ok bool) {
	d.cfg.Logf("transfer %s: ok=%v", transferID, ok)
	d.broadcast(&openairv1.DaemonEvent{
		Kind:       openairv1.DaemonEventKind_DAEMON_EVENT_KIND_TRANSFER_DONE,
		TransferId: transferID,
		Ok:         ok,
	})
}

// onClipboard is M5's entry point. Until then the request is answered rather
// than ignored, so a client learns the daemon cannot do it instead of waiting
// out its timeout.
func (d *Daemon) onClipboard(_ context.Context, c *client, payload []byte) {
	var req openairv1.DaemonClipboardRequest
	if !unmarshal(c, payload, &req) {
		return
	}
	_ = c.peer.ReplyError(req.GetRequestId(), session.CodeCapabilityUnavailable,
		"clipboard is not implemented in this build")
}

// trustLevelToWire converts a stored trust level to its schema value. The two
// scales differ by one and must never be cast (D-34, D-39).
func trustLevelToWire(l identity.TrustLevel) openairv1.TrustLevel {
	switch l {
	case identity.LevelUnpaired:
		return openairv1.TrustLevel_TRUST_LEVEL_UNPAIRED
	case identity.LevelTrusted:
		return openairv1.TrustLevel_TRUST_LEVEL_TRUSTED
	case identity.LevelOwned:
		return openairv1.TrustLevel_TRUST_LEVEL_OWNED
	default:
		return openairv1.TrustLevel_TRUST_LEVEL_UNSPECIFIED
	}
}

var errNoSuchDevice = errors.New("no such device")

func humanBytes(n uint64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := uint64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

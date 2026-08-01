package daemon

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sort"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/shreyashsri79/openair/internal/caps/clipboard"
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
	case ipc.MsgUnlockRequest:
		d.onUnlock(c, payload)
	case ipc.MsgLockRequest:
		d.onLock(c, payload)
	case ipc.MsgTrustRequest:
		d.onTrust(c, payload)
	case ipc.MsgBrowseRequest:
		d.onBrowse(ctx, c, payload)
	case ipc.MsgFetchRequest:
		d.onFetch(ctx, c, payload)
	case ipc.MsgStreamRequest:
		d.onStream(ctx, c, payload)
	case ipc.MsgInputRequest:
		d.onInput(ctx, c, payload)
	case ipc.MsgMirrorRequest:
		d.onMirror(ctx, c, payload)
	case ipc.MsgNotifyRequest:
		d.onNotify(ctx, c, payload)
	case ipc.MsgDismissRequest:
		d.onDismissRequest(ctx, c, payload)
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
		Announcing:     d.discovery() != nil && !d.cfg.NoAnnounce,
		AutoAccept:     d.cfg.AutoAccept,
		Subscribers:    uint32(subs),
		ProtectionTier: session.ProtectionTierToWire(d.id.ProtectionTier()),
		UnlockedDevices: func() []string {
			ids := d.id.UnlockedPeers()
			out := make([]string, 0, len(ids))
			for _, id := range ids {
				out = append(out, string(id))
			}
			sort.Strings(out)
			return out
		}(),
		KeySwappable:   d.id.Swappable(),
		RendezvousAddr: d.cfg.Rendezvous.Addr,
		RendezvousExpiresUnixMs: func() int64 {
			if c := d.rendezvousClient(); c != nil {
				at, _ := c.LastRegistration()
				return millisOrZero(at)
			}
			return 0
		}(),
		RelayAddr:      d.cfg.Relay.Addr,
		RelayConnected: d.relayPacketConn() != nil,
		RendezvousError: func() string {
			if c := d.rendezvousClient(); c != nil {
				if _, err := c.LastRegistration(); err != nil {
					return err.Error()
				}
			}
			return ""
		}(),
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
			DeviceId:            string(p.DeviceID),
			DisplayName:         p.DisplayName,
			Platform:            p.Platform,
			Paired:              true,
			Level:               trustLevelToWire(p.Level),
			SessionOpen:         open,
			ProtectionTier:      session.ProtectionTierToWire(p.ProtectionTier),
			PrivilegeKeyPinned:  len(p.PrivilegePublicKey) > 0,
			UnlockedUntilUnixMs: d.unlockedUntilMillis(p.DeviceID),
		}
		byID[p.DeviceID] = dev
		out = append(out, dev)
	}

	if disco := d.discovery(); disco != nil {
		for _, cand := range disco.Peers() {
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
	// The unattended path, and the reason M6 exists: an Owned peer that proved
	// possession of its privilege key for this exact offer does not need a human
	// (PRD R3, R11). The proof was verified by the session layer before this
	// call; OwnedFromContext is how that decision reaches here (§6).
	if peer.Level == identity.LevelOwned && session.OwnedFromContext(ctx) {
		d.cfg.Logf("accepting %s from owned device %s without asking",
			offer.TransferID, peer.DeviceID.Fingerprint())
		d.logAuth("unattended transfer accepted", peer.DeviceID, offer.TransferID)
		d.broadcast(&openairv1.DaemonEvent{
			Kind:       openairv1.DaemonEventKind_DAEMON_EVENT_KIND_TRANSFER_STARTED,
			DeviceId:   string(peer.DeviceID),
			TransferId: offer.TransferID,
			BytesTotal: offer.TotalBytes,
			Text:       "accepted unattended: owned device with a valid auth proof",
		})
		return true, nil
	}

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

// onClipboard pushes clipboard content to a device (§9, M5).
//
// The request carries the network `ClipboardPush` verbatim, which is the point
// of D-29: a tray UI pushing the clipboard emits the same bytes the wire
// carries, and there is no translation layer here to keep in sync.
func (d *Daemon) onClipboard(ctx context.Context, c *client, payload []byte) {
	var req openairv1.DaemonClipboardRequest
	if !unmarshal(c, payload, &req) {
		return
	}
	push := req.GetPush()
	if push == nil {
		_ = c.peer.ReplyError(req.GetRequestId(), 0, "no clipboard content given")
		return
	}

	sess, err := d.sessionTo(ctx, req.GetTarget())
	if err != nil {
		_ = c.peer.ReplyError(req.GetRequestId(), 0, "%v", err)
		return
	}
	if err := d.clip.Push(ctx, sess, push.GetMime(), push.GetContent()); err != nil {
		code := session.CodeNoError
		if pc, ok := session.ErrorCodeOf(err); ok {
			code = pc
		} else if errors.Is(err, clipboard.ErrTooLarge) {
			code = session.CodeResourceExhausted
		}
		_ = c.peer.ReplyError(req.GetRequestId(), code, "%v", err)
		return
	}
	_ = c.peer.Reply(ipc.MsgClipboardResponse, req.GetRequestId(), &openairv1.DaemonClipboardResponse{})
}

// onClipboardReceived applies an inbound push, or says why it could not.
//
// Failing to reach a system clipboard is not an error the peer should see: the
// content arrived and was accepted, and whether this machine has somewhere to
// paste it is not the sender's problem. It is reported as an event instead, so
// `openair watch` shows the text even on a headless box.
func (d *Daemon) onClipboardReceived(ctx context.Context, peer identity.Peer, content clipboard.Content) error {
	d.cfg.Logf("clipboard from %s: %d bytes", peer.DeviceID.Fingerprint(), len(content.Bytes))

	// M13's loop suppression, which applies whether or not the watcher is
	// running: content already here is not re-applied, and content older than
	// this device's own last copy loses a simultaneous edit (§9's origin_ts).
	if !d.clipState.ShouldApply(content.Text(), content.OriginTS) {
		return nil
	}
	// Recorded *before* the write: the write is a subprocess, and a watcher
	// poll landing in between would see peer content it had no record of and
	// send it straight back.
	d.clipState.Applied(content.Text())
	d.broadcast(&openairv1.DaemonEvent{
		Kind:     openairv1.DaemonEventKind_DAEMON_EVENT_KIND_CLIPBOARD,
		DeviceId: string(peer.DeviceID),
		Text:     content.Text(),
		Ok:       true,
	})

	// The OS write runs off this goroutine. It is a subprocess on most
	// platforms, and this one is the session's per-capability queue (D-41):
	// blocking it behind a clipboard helper would stall every later message
	// from the same peer.
	go func() {
		if err := clipboard.WriteOS(context.WithoutCancel(ctx), content.Text()); err != nil {
			d.cfg.Logf("clipboard from %s not applied: %v", peer.DeviceID.Fingerprint(), err)
		}
	}()
	return nil
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

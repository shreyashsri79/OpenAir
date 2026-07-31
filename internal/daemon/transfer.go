package daemon

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/shreyashsri79/openair/internal/caps/files"
	"github.com/shreyashsri79/openair/internal/discovery"
	"github.com/shreyashsri79/openair/internal/identity"
	"github.com/shreyashsri79/openair/internal/ipc"
	"github.com/shreyashsri79/openair/internal/pairing"
	"github.com/shreyashsri79/openair/internal/session"
	openairv1 "github.com/shreyashsri79/openair/internal/wire/openair/v1"
)

// resolveWait is how long a named target is given to appear on the network
// before the daemon gives up. Discovery converges in well under a second on a
// working LAN (PRD R6 asks for three); this is the allowance for a device that
// is simply not there.
const resolveWait = 5 * time.Second

// send offers files to a target, which is a device name, a DeviceID prefix, or
// an explicit host:port.
//
// A session already open with that device is reused. That is the daemon's
// advantage over the CLI dialling for itself: the second transfer to a device
// costs no handshake, and HLD §3.3's one-connection-per-peer stops being an
// aspiration.
func (d *Daemon) send(ctx context.Context, target string, paths []string) (string, identity.DeviceID, error) {
	items := make([]files.Item, 0, len(paths))
	for _, p := range paths {
		st, err := os.Stat(p)
		if err != nil {
			return "", "", err
		}
		if st.IsDir() {
			return "", "", fmt.Errorf("%s is a directory; directory transfer is not implemented yet", p)
		}
		items = append(items, files.Item{LocalPath: p, RelPath: filepath.Base(p)})
	}

	sess, err := d.sessionTo(ctx, target)
	if err != nil {
		return "", "", err
	}
	peer := sess.Peer()

	transferID, err := d.files.Send(ctx, sess, items)
	if err != nil {
		return transferID, peer.DeviceID, fmt.Errorf("transfer %s: %w", transferID, err)
	}
	return transferID, peer.DeviceID, nil
}

// sessionTo returns a session with the target, reusing a live one where it can.
func (d *Daemon) sessionTo(ctx context.Context, target string) (session.Session, error) {
	if !discovery.IsAddr(target) {
		if id, ok := d.resolvePaired(target); ok {
			if sess, live := d.sessionFor(id); live {
				return sess, nil
			}
		}
	}

	addrs, err := d.targetAddrs(ctx, target)
	if err != nil {
		// No usable address. A relay is exactly the case where there is none:
		// a device behind a NAT publishes no endpoint anyone can dial, and is
		// reached through its forwarder instead (M8, §17).
		if sess, relayErr := d.tryRelay(ctx, target); relayErr == nil {
			return sess, nil
		}
		return nil, err
	}

	sess, err := d.dialFirst(ctx, addrs)
	if err != nil {
		// Every direct path failed. The relay is the fallback, not the first
		// choice: it costs an extra hop and it tells its operator that these
		// two devices are talking.
		if relayed, relayErr := d.tryRelay(ctx, target); relayErr == nil {
			d.cfg.Logf("no direct path to %s; using the relay", target)
			return relayed, nil
		}
		// The far end refuses an unpaired peer during Hello (M2), so this dial
		// fails before the local check below is ever reached. The advice is the
		// same, and the caller needs it rather than a transport error.
		if code, ok := session.ErrorCodeOf(err); ok {
			switch code {
			case session.CodeNotPaired:
				return nil, fmt.Errorf("that device has not paired with this one; "+
					"run `openair pair` on both ends first: %w", err)
			case session.CodeKeyMismatch:
				return nil, fmt.Errorf("the device at that address is not the one you paired; "+
					"re-pair with `openair pair`: %w", err)
			}
		}
		return nil, err
	}

	// M2's rule on the sending side: files go to devices this one paired with
	// and no others. The receiving end enforces the same thing independently.
	peer := sess.Peer()
	if err := d.pairs.Authorize(peer); err != nil {
		sess.Close(uint16(session.CodeNotPaired), "not paired")
		if errors.Is(err, identity.ErrKeyMismatch) {
			return nil, fmt.Errorf("%w: the device at that address is not the one you paired; re-pair with `openair pair`", err)
		}
		return nil, fmt.Errorf("%s is not paired with this device; run `openair pair` on both ends first",
			peer.DeviceID.Fingerprint())
	}

	d.register(sess)
	return sess, nil
}

// resolvePaired matches a target against the trust store, so a device that is
// paired but momentarily invisible to discovery is still nameable.
func (d *Daemon) resolvePaired(target string) (identity.DeviceID, bool) {
	want := strings.ToLower(strings.ReplaceAll(target, "-", ""))
	for _, p := range d.store.List() {
		id := strings.ToLower(string(p.DeviceID))
		switch {
		case id == want,
			len(want) >= 4 && strings.HasPrefix(id, want),
			p.DisplayName != "" && strings.EqualFold(p.DisplayName, target):
			return p.DeviceID, true
		}
	}
	return "", false
}

// targetAddrs turns what the user typed into addresses to dial.
func (d *Daemon) targetAddrs(ctx context.Context, target string) ([]string, error) {
	if discovery.IsAddr(target) {
		return []string{target}, nil
	}

	// The local network first: it is faster, it needs no third party, and it is
	// the case that covers most transfers.
	if disco := d.discovery(); disco != nil {
		deadline := time.Now().Add(resolveWait)
		for {
			matches := discovery.Match(disco.Peers(), target)
			switch {
			case len(matches) == 1:
				return matches[0].Addrs, nil
			case len(matches) > 1:
				var b strings.Builder
				for _, m := range matches {
					fmt.Fprintf(&b, "\n  %s  %s", m.DeviceID.Fingerprint(), m.DisplayName)
				}
				return nil, fmt.Errorf("%q matches %d devices, name one exactly:%s", target, len(matches), b.String())
			}
			if time.Now().After(deadline) {
				break
			}
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(100 * time.Millisecond):
			}
		}
	}

	// Not on this network. A paired device may have published where it is
	// (M7, §16), which is the entire reason to run a rendezvous server: the
	// answer is verified against the key already pinned for that device, so
	// what arrives is an address to try rather than an authority to trust.
	if id, paired := d.resolvePaired(target); paired {
		if addrs, err := d.lookupPeer(ctx, id); err == nil && len(addrs) > 0 {
			d.cfg.Logf("found %s through the rendezvous server: %s",
				id.Fingerprint(), strings.Join(addrs, " "))
			return addrs, nil
		} else if err != nil && d.rendezvousClient() != nil {
			d.cfg.Logf("rendezvous lookup for %s: %v", id.Fingerprint(), err)
		}
	}

	if d.discovery() == nil && d.rendezvousClient() == nil {
		return nil, fmt.Errorf("%q is not a host:port, and neither discovery nor a rendezvous server is running", target)
	}
	return nil, fmt.Errorf("%w: no device matching %q is on the local network or published to the rendezvous server; "+
		"give an explicit host:port, or check the other device is running", errNoSuchDevice, target)
}

func (d *Daemon) dialFirst(ctx context.Context, addrs []string) (session.Session, error) {
	if len(addrs) == 0 {
		return nil, errors.New("no address to dial")
	}

	var lastErr error
	for _, addr := range addrs {
		udpAddr, err := net.ResolveUDPAddr("udp", addr)
		if err != nil {
			lastErr = fmt.Errorf("dial %s: %w", addr, err)
			continue
		}
		// Dialled out of the shared socket rather than one quic-go binds for
		// itself. That is what M9 needs: a NAT mapping belongs to a port, and
		// the port that gets punched has to be the port the session uses.
		//
		// An address is not an identity: nothing can be pinned before the
		// handshake, so the key is learned from it and checked immediately
		// afterwards. The zero Peer is what session.New reads as "unpinned".
		sess, err := d.endpoint.Dial(ctx, udpAddr, identity.Peer{})
		if err == nil {
			return sess, nil
		}
		if errors.Is(err, identity.ErrKeyMismatch) {
			return nil, fmt.Errorf("dial %s: %w", addr, err)
		}
		lastErr = fmt.Errorf("dial %s: %w", addr, err)
	}
	return nil, lastErr
}

// pair runs PROTOCOL.md §5 from whichever side the caller is on.
//
// An empty offer means this device displays one and waits for a peer to bring
// it back; a non-empty one means the user scanned or typed the peer's offer and
// this device dials it. Both end with the six digits in front of a human, via
// the prompt in confirmPairing.
func (d *Daemon) pair(ctx context.Context, c *client, offerText string) (identity.Peer, string, error) {
	if offerText == "" {
		return d.pairAwait(ctx, c)
	}
	peer, err := d.pairInitiate(ctx, offerText)
	return peer, "", err
}

// pairInitiate consumes an offer: dial the address in it, then run §5.2 as the
// initiator.
func (d *Daemon) pairInitiate(ctx context.Context, offerText string) (identity.Peer, error) {
	offer, err := pairing.DecodeOffer(offerText)
	if err != nil {
		return identity.Peer{}, fmt.Errorf("read pairing code: %w", err)
	}
	if len(offer.GetLanHints()) == 0 {
		return identity.Peer{}, errors.New("pairing code carries no address; the other device must be reachable")
	}

	// Pairing is the one time an unpaired peer may be admitted, and only while
	// this window is open (§5).
	closeWindow := d.pairs.OpenWindow()
	defer closeWindow()

	sess, err := d.dialFirst(ctx, offer.GetLanHints())
	if err != nil {
		return identity.Peer{}, err
	}
	defer d.pairs.Detach(sess)

	peer, err := d.pairs.Initiate(ctx, sess, offer)
	if err != nil {
		sess.Close(uint16(session.CodeRejected), "pairing failed")
		return identity.Peer{}, err
	}
	d.register(sess)
	d.broadcast(&openairv1.DaemonEvent{
		Kind:     openairv1.DaemonEventKind_DAEMON_EVENT_KIND_PAIRED,
		DeviceId: string(peer.DeviceID),
		Text:     peer.DisplayName,
		Ok:       true,
	})
	return peer, nil
}

// pairAwait displays this device's offer and waits for a peer to arrive with
// it.
//
// The offer reaches the client as an event rather than as the reply, because
// the reply cannot come until pairing finishes and the user needs the code
// before that: they have to carry it to the other device.
func (d *Daemon) pairAwait(ctx context.Context, c *client) (identity.Peer, string, error) {
	offer, err := pairing.NewOffer(d.id, []string{d.ln.Addr()})
	if err != nil {
		return identity.Peer{}, "", err
	}
	text, err := pairing.EncodeOffer(offer)
	if err != nil {
		return identity.Peer{}, "", err
	}

	closeWindow := d.pairs.OpenWindow()
	defer closeWindow()

	// The client gets the code immediately; the peer that answers it may take
	// as long as the user needs to walk to the other device.
	_ = c.peer.Send(ipc.MsgEvent, &openairv1.DaemonEvent{
		Kind: openairv1.DaemonEventKind_DAEMON_EVENT_KIND_PAIRED,
		Text: text,
	})

	sess, err := d.awaitPairingSession(ctx)
	if err != nil {
		return identity.Peer{}, text, err
	}
	defer d.pairs.Detach(sess)

	peer, err := d.pairs.Await(ctx, sess)
	if err != nil {
		return identity.Peer{}, text, err
	}
	d.broadcast(&openairv1.DaemonEvent{
		Kind:     openairv1.DaemonEventKind_DAEMON_EVENT_KIND_PAIRED,
		DeviceId: string(peer.DeviceID),
		Text:     peer.DisplayName,
		Ok:       true,
	})
	return peer, text, nil
}

// awaitPairingSession waits for the next session to appear.
//
// The accept loop owns the listener, so pairing cannot call Accept itself; it
// waits for a session to be registered instead. Any session will do: the
// pairing window is open, so an unpaired peer arriving now is the peer we are
// waiting for, and one that is already paired running §5 again is a re-pair.
func (d *Daemon) awaitPairingSession(ctx context.Context) (session.Session, error) {
	known := map[session.Session]bool{}
	d.mu.Lock()
	for _, s := range d.sessions {
		known[s] = true
	}
	d.mu.Unlock()

	for {
		d.mu.Lock()
		var found session.Session
		for _, s := range d.sessions {
			if !known[s] {
				found = s
				break
			}
		}
		d.mu.Unlock()
		if found != nil {
			return found, nil
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("no device arrived with the pairing code: %w", ctx.Err())
		case <-time.After(50 * time.Millisecond):
		}
	}
}

// tryRelay opens a relayed session to a paired device, and registers it.
//
// It answers only for a target that resolves to a paired DeviceID: a relayed
// path addresses a device rather than an address, and the pinned key is what
// makes the far end verifiable once it answers.
func (d *Daemon) tryRelay(ctx context.Context, target string) (session.Session, error) {
	if d.relayPacketConn() == nil {
		return nil, errors.New("no relay connection")
	}
	id, ok := d.resolvePaired(target)
	if !ok {
		return nil, fmt.Errorf("%q is not a paired device, so it cannot be reached through a relay", target)
	}
	sess, err := d.dialViaRelay(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := d.pairs.Authorize(sess.Peer()); err != nil {
		sess.Close(uint16(session.CodeNotPaired), "not paired")
		return nil, err
	}
	d.register(sess)
	return sess, nil
}

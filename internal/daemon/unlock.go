package daemon

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/shreyashsri79/openair/internal/identity"
	"github.com/shreyashsri79/openair/internal/ipc"
	"github.com/shreyashsri79/openair/internal/session"
	openairv1 "github.com/shreyashsri79/openair/internal/wire/openair/v1"
)

// Unlock, lock, and promotion: the daemon's half of M6.
//
// The daemon holds the privilege key and therefore holds the unlock session
// (D-19). A shell asks it to unlock and hands over the credential; nothing here
// prompts, because a daemon has no terminal and no screen. That puts the
// passphrase on the local socket, which is deliberate and bounded: the socket is
// already the boundary protecting the identity key this process holds, it is
// 0600 with a peer-credential check on Linux, and a process that could read the
// credential in flight could equally drive the unlocked daemon (D-58).

// authLogFile is the local session log PRD R4 requires, next to the keys it
// concerns. One JSON object per line, appended: this is a record for a human
// reading it after something happened, not a data store.
const authLogFile = "auth.log"

// onUnlock starts an unlock session for one peer (D-30's per-peer scope).
func (d *Daemon) onUnlock(c *client, payload []byte) {
	var req openairv1.DaemonUnlockRequest
	if !unmarshal(c, payload, &req) {
		return
	}
	target := identity.DeviceID(req.GetDeviceId())
	if _, ok := d.store.Get(target); !ok {
		_ = c.peer.ReplyError(req.GetRequestId(), session.CodeNotPaired,
			"no paired device %s; unlock authorises one device at a time", req.GetDeviceId())
		return
	}

	policy := identity.PolicyTimed
	if req.GetNeverExpire() {
		policy = identity.PolicyNever
	}
	expiry, err := d.id.Unlock(target, identity.UnlockOptions{
		Passphrase:  req.GetPassphrase(),
		KeystoreKEK: req.GetKeystoreKek(),
		Policy:      policy,
		Lifetime:    time.Duration(req.GetLifetimeMs()) * time.Millisecond,
	})
	if err != nil {
		_ = c.peer.ReplyError(req.GetRequestId(), unlockErrorCode(err), "%v", err)
		d.logAuth("unlock refused: "+err.Error(), target, "")
		return
	}

	// The trust store records when the token was granted, per D-18 and D-30, so
	// a device list can say what is unlocked without asking the running process.
	if peer, ok := d.store.Get(target); ok {
		peer.TokenGrantedAt = time.Now().UnixMilli()
		peer.AuthPolicy = policy
		if err := d.store.Put(peer); err != nil {
			d.cfg.Logf("recording the unlock against %s failed: %v", target.Fingerprint(), err)
		}
	}

	d.logAuth("unlocked", target, policy)
	d.broadcast(&openairv1.DaemonEvent{
		Kind:     openairv1.DaemonEventKind_DAEMON_EVENT_KIND_UNLOCKED,
		DeviceId: string(target),
		Text:     unlockText(expiry, policy),
		Ok:       true,
	})

	_ = c.peer.Reply(ipc.MsgUnlockResponse, req.GetRequestId(), &openairv1.DaemonUnlockResponse{
		ExpiresUnixMs:  millisOrZero(expiry),
		ProtectionTier: session.ProtectionTierToWire(d.id.ProtectionTier()),
		KeySwappable:   d.id.Swappable(),
	})
}

// onLock ends a session immediately. An empty device_id ends all of them, which
// is the "I am leaving this machine" action rather than a per-device one.
func (d *Daemon) onLock(c *client, payload []byte) {
	var req openairv1.DaemonLockRequest
	if !unmarshal(c, payload, &req) {
		return
	}
	if id := identity.DeviceID(req.GetDeviceId()); id != "" {
		d.id.LockPeer(id)
		d.logAuth("locked", id, "")
		d.broadcast(&openairv1.DaemonEvent{
			Kind:     openairv1.DaemonEventKind_DAEMON_EVENT_KIND_LOCKED,
			DeviceId: string(id),
			Text:     "unlock session ended",
		})
	} else {
		d.id.Lock()
		d.logAuth("locked every session", "", "")
		d.broadcast(&openairv1.DaemonEvent{
			Kind: openairv1.DaemonEventKind_DAEMON_EVENT_KIND_LOCKED,
			Text: "every unlock session ended; the privilege key is sealed again",
		})
	}
	_ = c.peer.Reply(ipc.MsgLockResponse, req.GetRequestId(), &openairv1.DaemonLockResponse{})
}

// onTrust promotes or demotes a paired device (§6.4).
//
// Promotion is a deliberate local act and never a response to a request, so
// there is no wire message that reaches this: it arrives from a shell, driven by
// the person sitting at this machine (PRD R3).
func (d *Daemon) onTrust(c *client, payload []byte) {
	var req openairv1.DaemonTrustRequest
	if !unmarshal(c, payload, &req) {
		return
	}
	peer, ok := d.store.Get(identity.DeviceID(req.GetDeviceId()))
	if !ok {
		_ = c.peer.ReplyError(req.GetRequestId(), session.CodeNotPaired,
			"no paired device %s", req.GetDeviceId())
		return
	}

	level := session.TrustLevelFromWire(req.GetLevel())
	if level == identity.LevelOwned {
		// D-21 tier 3, enforced where a user can be told why rather than deep in
		// a validation error: a device that does not protect a privilege key
		// must not be handed unattended access to this one.
		if len(peer.PrivilegePublicKey) == 0 {
			_ = c.peer.ReplyError(req.GetRequestId(), session.CodeUnauthorised,
				"%s has no privilege key pinned here -- pair the two devices again so the key is exchanged, then promote",
				peer.DeviceID.Fingerprint())
			return
		}
		if peer.ProtectionTier == identity.TierNone {
			_ = c.peer.ReplyError(req.GetRequestId(), session.CodeUnauthorised,
				"%s protects no privilege key (tier 3), so it cannot hold Owned access",
				peer.DeviceID.Fingerprint())
			return
		}
	}

	previous := peer.Level
	peer.Level = level
	if p := req.GetAuthPolicy(); p != "" {
		peer.AuthPolicy = p
	}
	if err := d.store.Put(peer); err != nil {
		_ = c.peer.ReplyError(req.GetRequestId(), 0, "%v", err)
		return
	}

	// Demotion takes effect mid-session (§6.1): the level is read per message,
	// so an Owned operation already refused at the next request. Any live unlock
	// for this peer is ended too, so nothing signs for a device that is no
	// longer owned.
	if level < identity.LevelOwned {
		d.id.LockPeer(peer.DeviceID)
	}

	d.logAuth(fmt.Sprintf("trust level %v -> %v", previous, level), peer.DeviceID, peer.AuthPolicy)
	_ = c.peer.Reply(ipc.MsgTrustResponse, req.GetRequestId(),
		&openairv1.DaemonTrustResponse{Level: trustLevelToWire(level)})
}

// unlockedUntilMillis reports a peer's unlock expiry for the device list: zero
// when nothing is unlocked, and -1 under the "never" policy, which has no
// expiry to report but is emphatically not "locked".
func (d *Daemon) unlockedUntilMillis(id identity.DeviceID) int64 {
	expiry, ok := d.id.UnlockedUntil(id)
	switch {
	case !ok:
		return 0
	case expiry.IsZero():
		return -1
	default:
		return expiry.UnixMilli()
	}
}

// onAuthEvent receives every authorisation decision the session layer makes for
// an inbound message (§6.3). Refusals are worth a user's attention; grants are
// worth a log line and nothing more, or an Owned peer doing ordinary work would
// bury everything else.
func (d *Daemon) onAuthEvent(ev session.AuthEvent) {
	if ev.Allowed {
		d.logAuth(fmt.Sprintf("owned request served: capID %d msgType %d", ev.CapID, ev.MsgType), ev.Peer, "")
		return
	}
	d.cfg.Logf("refused capID %d msgType %d from %s: %s (%s)",
		ev.CapID, ev.MsgType, ev.Peer.Fingerprint(), ev.Reason, ev.Code.String())
	d.logAuth(fmt.Sprintf("refused capID %d msgType %d: %s", ev.CapID, ev.MsgType, ev.Reason), ev.Peer, ev.Code.String())
	d.broadcast(&openairv1.DaemonEvent{
		Kind:     openairv1.DaemonEventKind_DAEMON_EVENT_KIND_AUTH_REFUSED,
		DeviceId: string(ev.Peer),
		Text:     fmt.Sprintf("%s (%s)", ev.Reason, ev.Code.String()),
	})
}

// onExpiryWarning is §6.5's fifteen-minute notice, so a long transfer can be
// extended deliberately rather than discovered broken.
func (d *Daemon) onExpiryWarning(target identity.DeviceID, expires time.Time) {
	d.cfg.Logf("unlock for %s expires at %s", target.Fingerprint(), expires.Format(time.Kitchen))
	d.logAuth("unlock expiring", target, expires.Format(time.RFC3339))
	d.broadcast(&openairv1.DaemonEvent{
		Kind:     openairv1.DaemonEventKind_DAEMON_EVENT_KIND_UNLOCK_EXPIRING,
		DeviceId: string(target),
		Text: fmt.Sprintf("unlock for %s expires in %s",
			target.Fingerprint(), time.Until(expires).Round(time.Minute)),
	})
}

// logAuth appends one line to the local session log (PRD R4, §6.3).
//
// Best effort by design: a daemon that stopped serving because its log file was
// unwritable would fail the user harder than the missing line does. The failure
// still goes to the operational log rather than nowhere.
func (d *Daemon) logAuth(what string, peer identity.DeviceID, detail string) {
	entry := struct {
		At     string `json:"at"`
		Event  string `json:"event"`
		Peer   string `json:"peer,omitempty"`
		Detail string `json:"detail,omitempty"`
	}{
		At:     time.Now().UTC().Format(time.RFC3339),
		Event:  what,
		Peer:   string(peer),
		Detail: detail,
	}
	line, err := json.Marshal(entry)
	if err != nil {
		return
	}
	path := filepath.Join(d.keyDir, authLogFile)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		d.cfg.Logf("cannot write the session log at %s: %v", path, err)
		return
	}
	defer f.Close()
	if _, err := f.Write(append(line, '\n')); err != nil {
		d.cfg.Logf("cannot write the session log at %s: %v", path, err)
	}
}

func unlockErrorCode(err error) session.ErrorCode {
	switch {
	case errors.Is(err, identity.ErrNoPrivilegeKey):
		return session.CodeUnauthorised
	case errors.Is(err, identity.ErrPassphrase), errors.Is(err, identity.ErrThrottled):
		return session.CodeUnauthorised
	case errors.Is(err, identity.ErrLocked):
		return session.CodeAuthExpired
	default:
		return 0
	}
}

func unlockText(expiry time.Time, policy string) string {
	if policy == identity.PolicyNever || expiry.IsZero() {
		return "unlocked until locked by hand (always-on)"
	}
	return fmt.Sprintf("unlocked until %s", expiry.Format(time.Kitchen))
}

func millisOrZero(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}

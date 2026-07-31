package identity

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"sync"
	"time"
)

// Unlock sessions: D-18's six-hour token, made concrete by D-19 as the lifetime
// of the decrypted privilege key in memory, and scoped per peer by D-30.
//
// Three properties this file exists to hold, all of them load-bearing:
//
//   - The token is not a boolean. Expiry wipes the decrypted key, so a expired
//     token cannot be bypassed by patching out a time comparison -- there is
//     nothing left to sign with. That is the whole point of D-19.
//   - Scope is per peer. One unlock authorises Owned operations against one
//     paired device, which is what lets the prompt name what it grants (D-30).
//     It remains policy rather than cryptography: one decrypted key can sign for
//     anyone, and D-30 records the delegation design that would close that gap.
//   - Expiry blocks new operations and does not abort running ones, capped at
//     one hour (PROTOCOL.md §6.5). The cap is enforced here, on the initiator,
//     because this is the only end that knows when the token expires.
const (
	// DefaultTokenLifetime is D-18's six hours, absolute from grant. It is not
	// a sliding window: an attacker who keeps a session busy would otherwise
	// never be locked out, which inverts the purpose of the timer.
	DefaultTokenLifetime = 6 * time.Hour

	// MaxInFlightGrace caps how long an operation authorised before expiry may
	// keep running afterwards (§6.5). Without a cap, starting a long operation
	// just before expiry would extend access indefinitely.
	MaxInFlightGrace = time.Hour

	// DefaultExpiryWarning is how long before expiry the user is told, so a long
	// transfer can be extended deliberately rather than discovered broken
	// (§6.5, D-25).
	DefaultExpiryWarning = 15 * time.Minute
)

// Auth policies recorded per peer (D-18). "never" is also D-20's always-on
// designation: one user-visible toggle, not two.
const (
	PolicyTimed = "timed"
	PolicyNever = "never"
)

var (
	// ErrGraceExpired reports an operation that outlived both its token and
	// §6.5's one-hour grace. Distinct from ErrLocked so a caller can say
	// "this ran too long" rather than "unlock again", which is the wrong advice
	// for work already in progress.
	ErrGraceExpired = errors.New("identity: operation outlived the unlock token and its one-hour grace")

	// ErrThrottled reports too many failed passphrase attempts in a row. It
	// protects the interactive path only -- an attacker holding the sealed file
	// can grind it elsewhere, which is why D-21 has tiers at all.
	ErrThrottled = errors.New("identity: too many failed unlock attempts, wait before retrying")
)

// Passphrase throttling. Attempts are counted per process, and the delay grows
// with consecutive failures up to a ceiling. This is D-19's "rate limiting on
// the PIN path": it slows someone typing at a keyboard and does nothing at all
// against an offline attack on a copied file.
const (
	throttleAfter   = 3
	throttleStep    = 2 * time.Second
	throttleCeiling = 30 * time.Second
)

// UnlockOptions carries the credential and the policy for one unlock.
type UnlockOptions struct {
	// Passphrase opens a TierPassphrase key. Ignored at TierKeystore.
	Passphrase []byte

	// KeystoreKEK is the 32-byte key-encryption key for TierKeystore, obtained
	// from the platform keystore after the user-presence challenge. Ignored at
	// TierPassphrase.
	//
	// Supplying it as a value rather than a callback is deliberate: the shell
	// that ran the biometric prompt is the only thing that can produce it, and
	// this package must not be able to obtain one without a user present.
	KeystoreKEK []byte

	// Policy is PolicyTimed (default) or PolicyNever. PolicyNever is D-20's
	// always-on designation and must be as deliberate an act as promotion to
	// Owned: nothing here makes it deliberate, so the caller owes the user that
	// prompt.
	Policy string

	// Lifetime overrides DefaultTokenLifetime for PolicyTimed. Zero means the
	// default. Ignored under PolicyNever.
	Lifetime time.Duration
}

// Unlocker is the unlock surface of an Identity. It is a separate interface
// from Identity because most callers -- everything that only dials, sends and
// receives -- have no business unlocking anything, and because a test double
// standing in for an Identity should not have to implement it.
type Unlocker interface {
	Unlock(target DeviceID, opts UnlockOptions) (expiry time.Time, err error)
	Lock()
	LockPeer(target DeviceID)
	UnlockedUntil(target DeviceID) (time.Time, bool)
	UnlockedPeers() []DeviceID
	ProtectionTier() ProtectionTier
}

var _ Unlocker = (*FileIdentity)(nil)

// grant is one peer's live unlock (D-30).
//
// The two timers are not what makes expiry correct -- every read of the state
// drops lapsed grants first, so a stopped clock cannot extend a token. They
// exist so the key does not linger in memory until somebody happens to ask, and
// so the 15-minute warning reaches a user who is not currently doing anything.
type grant struct {
	expires time.Time // zero means PolicyNever
	warn    *time.Timer
	reap    *time.Timer
}

func (g *grant) stopTimers() {
	if g.warn != nil {
		g.warn.Stop()
	}
	if g.reap != nil {
		g.reap.Stop()
	}
}

func (g *grant) live(now time.Time) bool {
	return g.expires.IsZero() || now.Before(g.expires)
}

// unlockState is the decrypted key and the grants that justify holding it.
//
// The key is held once, not per peer: it is one Ed25519 key and copying it per
// grant would multiply the thing this design tries to keep scarce. It is wiped
// the moment the last grant lapses.
type unlockState struct {
	mu     sync.Mutex
	key    *lockedKey
	grants map[DeviceID]*grant

	failures  int
	nextRetry time.Time
}

// Unlock decrypts the privilege key if it is not already held and grants Owned
// access to target for the token lifetime (D-18, D-30).
//
// It returns the expiry, or the zero time under PolicyNever. Unlocking a peer
// that is already unlocked replaces its grant, which is how a user extends a
// session before the 15-minute warning runs out.
func (i *FileIdentity) Unlock(target DeviceID, opts UnlockOptions) (time.Time, error) {
	if !target.Valid() {
		return time.Time{}, fmt.Errorf("identity: unlock target %q is not a DeviceID", target)
	}
	if i.tier == TierNone || i.privilegePub == nil {
		return time.Time{}, ErrNoPrivilegeKey
	}

	st := &i.unlock
	st.mu.Lock()
	defer st.mu.Unlock()

	now := i.now()
	st.expireLocked(now)

	if err := st.throttleLocked(now); err != nil {
		return time.Time{}, err
	}

	if st.key == nil {
		priv, err := i.openPrivilege(opts.Passphrase, opts.KeystoreKEK)
		if err != nil {
			if errors.Is(err, ErrPassphrase) {
				st.failures++
				st.nextRetry = now.Add(throttleDelay(st.failures))
			}
			return time.Time{}, err
		}
		key, lockErr := newLockedKey(priv)
		// Zero the caller-side copy regardless: openPrivilege returned a slice
		// the AEAD allocated on the Go heap, and it is not the copy we keep.
		zero(priv)
		if lockErr != nil {
			// Failing to lock pages is not a reason to refuse the unlock -- the
			// user would simply be unable to use Owned access at all -- but it
			// is a real degradation and Swappable reports it.
			i.log("privilege key pages could not be locked into RAM: %v", lockErr)
		}
		st.key = key
	}
	st.failures = 0
	st.nextRetry = time.Time{}

	g := &grant{}
	if opts.Policy != PolicyNever {
		lifetime := opts.Lifetime
		if lifetime <= 0 {
			lifetime = DefaultTokenLifetime
		}
		g.expires = now.Add(lifetime)
		if warn := i.warnBefore; warn > 0 && lifetime > warn && i.onExpiryWarning != nil {
			expires := g.expires
			g.warn = time.AfterFunc(lifetime-warn, func() {
				i.onExpiryWarning(target, expires)
			})
		}
		g.reap = time.AfterFunc(lifetime, i.sweep)
	}

	if prev, ok := st.grants[target]; ok {
		prev.stopTimers()
	}
	st.grants[target] = g
	return g.expires, nil
}

// Lock ends every unlock session and wipes the decrypted key immediately.
func (i *FileIdentity) Lock() {
	st := &i.unlock
	st.mu.Lock()
	defer st.mu.Unlock()
	for id, g := range st.grants {
		g.stopTimers()
		delete(st.grants, id)
	}
	st.wipeLocked()
}

// LockPeer ends one peer's unlock session (D-30's scope, exercised). The key is
// wiped once no peer is left holding a grant.
func (i *FileIdentity) LockPeer(target DeviceID) {
	st := &i.unlock
	st.mu.Lock()
	defer st.mu.Unlock()
	if g, ok := st.grants[target]; ok {
		g.stopTimers()
		delete(st.grants, target)
	}
	if len(st.grants) == 0 {
		st.wipeLocked()
	}
}

// UnlockedUntil reports when target's unlock expires and whether one is live.
// A live grant under PolicyNever reports the zero time with ok true.
func (i *FileIdentity) UnlockedUntil(target DeviceID) (time.Time, bool) {
	st := &i.unlock
	st.mu.Lock()
	defer st.mu.Unlock()
	st.expireLocked(i.now())
	g, ok := st.grants[target]
	if !ok {
		return time.Time{}, false
	}
	return g.expires, true
}

// UnlockedPeers lists the peers with a live unlock session, for a status
// display that can name what is currently authorised.
func (i *FileIdentity) UnlockedPeers() []DeviceID {
	st := &i.unlock
	st.mu.Lock()
	defer st.mu.Unlock()
	st.expireLocked(i.now())
	out := make([]DeviceID, 0, len(st.grants))
	for id := range st.grants {
		out = append(out, id)
	}
	return out
}

// Swappable reports whether the decrypted privilege key is sitting in pages
// that the kernel may write to swap.
//
// It is false in the normal case and true when mlock was refused, usually by
// RLIMIT_MEMLOCK. Callers should surface it rather than swallow it: the
// difference between "the key is in RAM for six hours" and "the key may be
// written to disk" is the difference between two threat models.
func (i *FileIdentity) Swappable() bool {
	st := &i.unlock
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.key != nil && !st.key.locked
}

// SignOwned produces the AuthProof signature for one Owned-level request
// (PROTOCOL.md §6). It fails with ErrLocked unless a live unlock session exists
// for this specific target (D-30).
func (i *FileIdentity) SignOwned(target DeviceID, capID byte, msgType uint16) (nonce []byte, issuedAt int64, sig []byte, err error) {
	if i.tier == TierNone || i.privilegePub == nil {
		return nil, 0, nil, ErrNoPrivilegeKey
	}

	nonce = make([]byte, OwnedNonceLen)
	if _, err := rand.Read(nonce); err != nil {
		return nil, 0, nil, fmt.Errorf("identity: auth proof nonce: %w", err)
	}

	st := &i.unlock
	st.mu.Lock()
	defer st.mu.Unlock()

	now := i.now()
	st.expireLocked(now)
	if _, ok := st.grants[target]; !ok || st.key == nil {
		return nil, 0, nil, fmt.Errorf("%w: no unlock session for %s", ErrLocked, target)
	}

	issuedAt = now.UnixMilli()
	input, err := OwnedSigningInput(target, capID, msgType, nonce, issuedAt)
	if err != nil {
		return nil, 0, nil, err
	}
	// The signature is made while the lock is held, not against a key read out
	// from under it. Expiry wipes the key in place, so a signature racing the
	// sweeper would either be a data race or -- worse and silently -- a
	// signature made with a half-zeroed key.
	return nonce, issuedAt, ed25519.Sign(st.key.key, input), nil
}

// Operation is one Owned-level action, held for its duration.
//
// It exists for §6.5: expiry must reject *new* operations while letting running
// ones finish, and that distinction cannot be made by a timer alone -- something
// has to know an operation is still going. Begin it before the first request,
// call Check before each further step, and End it when done.
type Operation struct {
	id       *FileIdentity
	target   DeviceID
	deadline time.Time // zero under PolicyNever: no expiry, so no grace to cap
	ended    bool
}

// BeginOwned starts an Owned-level operation against target, or reports why it
// cannot: ErrNoPrivilegeKey at tier 3 (D-21), ErrLocked with no live session.
func (i *FileIdentity) BeginOwned(target DeviceID) (*Operation, error) {
	if i.tier == TierNone || i.privilegePub == nil {
		return nil, ErrNoPrivilegeKey
	}
	st := &i.unlock
	st.mu.Lock()
	defer st.mu.Unlock()
	st.expireLocked(i.now())
	g, ok := st.grants[target]
	if !ok {
		return nil, fmt.Errorf("%w: no unlock session for %s", ErrLocked, target)
	}
	op := &Operation{id: i, target: target}
	if !g.expires.IsZero() {
		op.deadline = g.expires.Add(MaxInFlightGrace)
	}
	return op, nil
}

// Check reports whether the operation may continue.
//
// It stays nil past expiry -- work authorised when it began is not destroyed by
// the timer -- until the one-hour grace runs out, after which it is
// ErrGraceExpired and the caller must abort. New operations are refused at
// expiry by BeginOwned, so the two rules together are §6.5.
func (op *Operation) Check() error {
	if op.ended {
		return errors.New("identity: operation already ended")
	}
	if op.deadline.IsZero() {
		return nil
	}
	if op.id.now().After(op.deadline) {
		return fmt.Errorf("%w: %s", ErrGraceExpired, op.target)
	}
	return nil
}

// Deadline is when Check starts failing: expiry plus §6.5's grace, or the zero
// time under PolicyNever.
func (op *Operation) Deadline() time.Time { return op.deadline }

// End releases the operation. Safe to call twice.
func (op *Operation) End() { op.ended = true }

// --- internals ---------------------------------------------------------------

// expireLocked drops lapsed grants and wipes the key once none remain. Called
// on every path that reads the state, so expiry needs no timer to be correct --
// the sweeper exists only so the key does not sit in RAM until somebody asks.
func (s *unlockState) expireLocked(now time.Time) {
	for id, g := range s.grants {
		if !g.live(now) {
			g.stopTimers()
			delete(s.grants, id)
		}
	}
	if len(s.grants) == 0 {
		s.wipeLocked()
	}
}

func (s *unlockState) wipeLocked() {
	if s.key != nil {
		s.key.wipe()
		s.key = nil
	}
}

func (s *unlockState) throttleLocked(now time.Time) error {
	if s.failures >= throttleAfter && now.Before(s.nextRetry) {
		return fmt.Errorf("%w: %s remaining", ErrThrottled, s.nextRetry.Sub(now).Round(time.Second))
	}
	return nil
}

func throttleDelay(failures int) time.Duration {
	if failures < throttleAfter {
		return 0
	}
	d := time.Duration(failures-throttleAfter+1) * throttleStep
	if d > throttleCeiling {
		return throttleCeiling
	}
	return d
}

func (i *FileIdentity) now() time.Time {
	if i.clock != nil {
		return i.clock()
	}
	return time.Now()
}

func (i *FileIdentity) log(format string, args ...any) {
	if i.logf != nil {
		i.logf(format, args...)
	}
}

// sweep drops lapsed grants without waiting for a caller. It runs on a real
// ticker even when a test clock is installed, because what it protects is the
// key's residence in memory rather than any decision.
func (i *FileIdentity) sweep() {
	st := &i.unlock
	st.mu.Lock()
	defer st.mu.Unlock()
	st.expireLocked(i.now())
}

func zero(b []byte) {
	for i := range b {
		b[i] = 0
	}
}

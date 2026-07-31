package rendezvous

import (
	"context"
	"crypto/ed25519"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/shreyashsri79/openair/internal/identity"
	openairv1 "github.com/shreyashsri79/openair/internal/wire/openair/v1"
)

// DefaultLifetime is how long a registration asks to live. It is under §16's
// ten-minute cap on purpose: a client that asks for exactly the maximum has no
// room to be refused for clock skew, and the point of the cap is short-lived
// entries rather than maximal ones.
const DefaultLifetime = 5 * time.Minute

// ErrNotFound reports a lookup for a device the server has no live entry for.
// It is not a failure of the lookup: the usual cause is a device that is simply
// offline.
var ErrNotFound = errors.New("rendezvous: no live registration for that device")

// ErrServerKeyMismatch reports that the server presented a key deriving a
// DeviceID other than the pinned one. Same rule as a peer (§2), same response:
// do not retry, because it will not change back.
var ErrServerKeyMismatch = errors.New("rendezvous: server key does not match the pinned DeviceID")

// ClientConfig configures a Client.
type ClientConfig struct {
	// Local is this device's identity: it terminates TLS and signs
	// registrations. The identity key, not the privilege key — a device that
	// stopped being findable when its unlock expired would be unreachable for
	// exactly the reason D-20 exists to prevent.
	Local identity.Identity

	// Addr is the server's "host:port".
	Addr string

	// ServerID is the DeviceID the server's key must derive. It is required:
	// without it any host that answers the address is the rendezvous server,
	// and lookups could be answered by whoever won the race for the name.
	// Endpoints are not secret, but a lookup answered by an attacker is a
	// device that cannot be reached.
	ServerID identity.DeviceID

	// Lifetime is how long each registration asks for. Zero means
	// DefaultLifetime; anything over §16's cap is refused by the server.
	Lifetime time.Duration

	// DialTimeout bounds one connection attempt. Zero means ten seconds.
	DialTimeout time.Duration

	Logf func(format string, args ...any)
}

// Client talks to one rendezvous server (§16).
//
// It holds no connection between calls. A rendezvous exchange is two frames
// every few minutes, and a pooled connection would trade that for a socket to
// keep alive, reconnect and reason about across sleep and network changes.
type Client struct {
	cfg ClientConfig

	mu       sync.Mutex
	lastAck  time.Time
	lastFail error
}

// NewClient builds a client. It connects nothing until asked.
func NewClient(cfg ClientConfig) (*Client, error) {
	switch {
	case cfg.Local == nil:
		return nil, errors.New("rendezvous: ClientConfig.Local is required")
	case cfg.Addr == "":
		return nil, errors.New("rendezvous: ClientConfig.Addr is required")
	case !cfg.ServerID.Valid():
		return nil, fmt.Errorf("rendezvous: ClientConfig.ServerID %q is not a DeviceID", cfg.ServerID)
	}
	if cfg.Lifetime <= 0 {
		cfg.Lifetime = DefaultLifetime
	}
	if cfg.DialTimeout <= 0 {
		cfg.DialTimeout = 10 * time.Second
	}
	if cfg.Logf == nil {
		cfg.Logf = func(string, ...any) {}
	}
	return &Client{cfg: cfg}, nil
}

// Register publishes this device's endpoints and returns when they expire.
func (c *Client) Register(ctx context.Context, endpoints []string, relayHome string) (time.Time, error) {
	conn, err := c.dial(ctx)
	if err != nil {
		return time.Time{}, err
	}
	defer conn.Close()

	now := time.Now()
	issuedAt := now.UnixMilli()
	expiresAt := now.Add(c.cfg.Lifetime).UnixMilli()

	signer, ok := c.cfg.Local.(interface {
		SignRegistration(endpoints []string, relayHome string, issuedAt, expiresAt int64) ([]byte, error)
	})
	if !ok {
		return time.Time{}, errors.New("rendezvous: identity cannot sign registrations")
	}
	sig, err := signer.SignRegistration(endpoints, relayHome, issuedAt, expiresAt)
	if err != nil {
		return time.Time{}, err
	}

	reg := &openairv1.Registration{
		DeviceId:  string(c.cfg.Local.DeviceID()),
		Endpoints: endpoints,
		RelayHome: relayHome,
		IssuedAt:  issuedAt,
		ExpiresAt: expiresAt,
		Signature: sig,
	}
	if err := writeMessage(conn, MsgRegister, reg); err != nil {
		return time.Time{}, err
	}
	var ack openairv1.RegistrationAck
	if err := readInto(conn, MsgRegisterAck, &ack); err != nil {
		c.note(time.Time{}, err)
		return time.Time{}, err
	}

	// The server's expiry, not ours: §16 lets it grant less than was asked for,
	// and heartbeating against what we requested would let an entry lapse while
	// the client believed it was live.
	expires := time.UnixMilli(ack.GetExpiresAt())
	c.note(expires, nil)
	return expires, nil
}

// Lookup asks where a device is, and verifies the answer itself.
//
// peerKey is the key already pinned for that device (§2). The verification is
// the point: a rendezvous server that returns a forged endpoint list should
// change nothing, and it changes nothing only if the client checks the
// signature rather than trusting the answer.
func (c *Client) Lookup(ctx context.Context, target identity.DeviceID, peerKey ed25519.PublicKey) ([]string, string, error) {
	conn, err := c.dial(ctx)
	if err != nil {
		return nil, "", err
	}
	defer conn.Close()

	if err := writeMessage(conn, MsgLookupRequest, &openairv1.LookupRequest{DeviceId: string(target)}); err != nil {
		return nil, "", err
	}
	var resp openairv1.LookupResponse
	if err := readInto(conn, MsgLookupResponse, &resp); err != nil {
		return nil, "", err
	}
	if !resp.GetFound() || resp.GetRegistration() == nil {
		return nil, "", fmt.Errorf("%w: %s", ErrNotFound, target)
	}

	reg := resp.GetRegistration()
	if identity.DeviceID(reg.GetDeviceId()) != target {
		return nil, "", fmt.Errorf("rendezvous: asked for %s, the server answered for %s", target, reg.GetDeviceId())
	}
	if err := identity.VerifyRegistration(peerKey, target, reg.GetEndpoints(), reg.GetRelayHome(),
		reg.GetIssuedAt(), reg.GetExpiresAt(), reg.GetSignature()); err != nil {
		return nil, "", fmt.Errorf("rendezvous: registration for %s is not signed by its key: %w", target, err)
	}
	if expires := time.UnixMilli(reg.GetExpiresAt()); !expires.After(time.Now()) {
		// The server should have swept it. Refusing an expired entry here as
		// well means a server that keeps stale records cannot make this client
		// dial an address the device gave up minutes ago.
		return nil, "", fmt.Errorf("%w: %s (the entry expired %s ago)",
			ErrNotFound, target, time.Since(expires).Round(time.Second))
	}
	return reg.GetEndpoints(), reg.GetRelayHome(), nil
}

// KeepRegistered re-registers until ctx is cancelled, at two thirds of whatever
// lifetime the server grants.
//
// Failures are retried rather than fatal: the usual cause is a network that has
// gone away, which is also the moment the endpoints being published are about
// to change anyway.
func (c *Client) KeepRegistered(ctx context.Context, endpoints func() ([]string, string)) {
	const minRetry = 15 * time.Second

	for {
		eps, relayHome := endpoints()
		wait := minRetry

		expires, err := c.Register(ctx, eps, relayHome)
		if err != nil {
			c.cfg.Logf("rendezvous registration failed: %v", err)
		} else if d := time.Until(expires); d > 0 {
			wait = time.Duration(float64(d) * 2 / 3)
			if wait < minRetry {
				wait = minRetry
			}
		}

		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
}

// LastRegistration reports when this client last successfully registered and
// what the last failure was, for a status line that can say whether a device is
// actually findable.
func (c *Client) LastRegistration() (time.Time, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lastAck, c.lastFail
}

func (c *Client) note(expires time.Time, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err != nil {
		c.lastFail = err
		return
	}
	c.lastAck = expires
	c.lastFail = nil
}

// dial opens a TLS connection and checks the server is the one pinned.
func (c *Client) dial(ctx context.Context) (*tls.Conn, error) {
	dialCtx, cancel := context.WithTimeout(ctx, c.cfg.DialTimeout)
	defer cancel()

	var d net.Dialer
	nc, err := d.DialContext(dialCtx, "tcp", c.cfg.Addr)
	if err != nil {
		return nil, fmt.Errorf("rendezvous: dial %s: %w", c.cfg.Addr, err)
	}

	tlsConf, observed, err := identityPairingConfig(c.cfg.Local)
	if err != nil {
		nc.Close()
		return nil, err
	}
	conn := tls.Client(nc, tlsConf)
	if err := conn.HandshakeContext(dialCtx); err != nil {
		nc.Close()
		return nil, fmt.Errorf("rendezvous: handshake with %s: %w", c.cfg.Addr, err)
	}

	key := observed.Key()
	if len(key) != ed25519.PublicKeySize {
		conn.Close()
		return nil, fmt.Errorf("rendezvous: %s presented no usable key", c.cfg.Addr)
	}
	if got := identity.DeriveDeviceID(key); got != c.cfg.ServerID {
		conn.Close()
		return nil, fmt.Errorf("%w: pinned %s, got %s", ErrServerKeyMismatch, c.cfg.ServerID, got)
	}
	return conn, nil
}

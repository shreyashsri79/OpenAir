package identity

import (
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"sort"
	"sync"
)

// trustStoreVersion versions the on-disk schema independently of the wire
// protocol. The record itself is the consolidated list in D-30.
const trustStoreVersion = 1

// ErrUnknownPeer reports a Delete for a DeviceID the store does not hold.
var ErrUnknownPeer = errors.New("identity: no such peer")

// FileTrustStore is a TrustStore kept in memory and mirrored to one JSON file.
//
// Safe for concurrent use: the daemon reads it on every inbound connection, so
// reads take a shared lock and never touch the disk. Writes are rare -- pairing,
// revocation, a last-seen update -- and serialise behind the write lock, each
// one rewriting the file atomically. The whole store is a handful of peers; a
// rewrite is cheaper than the bookkeeping any incremental format would need.
//
// Every Peer crossing the boundary is deep-copied. The struct carries three
// byte slices, and handing callers the store's own backing arrays would make
// "safe for concurrent use" true only until somebody wrote through one.
type FileTrustStore struct {
	path string

	mu    sync.RWMutex
	peers map[DeviceID]Peer
}

var _ TrustStore = (*FileTrustStore)(nil)

type trustStoreFile struct {
	Version int          `json:"version"`
	Peers   []storedPeer `json:"peers"`
}

// storedPeer is the persisted form. It is a separate type from Peer so that
// renaming a domain field cannot silently orphan every record on disk: the JSON
// names live here and nowhere else.
type storedPeer struct {
	DeviceID            DeviceID `json:"device_id"`
	IdentityPublicKey   []byte   `json:"identity_public_key"`
	PrivilegePublicKey  []byte   `json:"privilege_public_key,omitempty"`
	DisplayName         string   `json:"display_name"`
	Platform            string   `json:"platform"`
	Level               int      `json:"level"`
	GrantedCapabilities []byte   `json:"granted_capabilities,omitempty"`
	AuthPolicy          string   `json:"auth_policy"`
	TokenGrantedAt      int64    `json:"token_granted_at"`
	ProtectionTier      int      `json:"protection_tier"`
	CreatedAt           int64    `json:"created_at"`
	LastSeen            int64    `json:"last_seen"`
}

func toStored(p Peer) storedPeer {
	return storedPeer{
		DeviceID:            p.DeviceID,
		IdentityPublicKey:   p.IdentityPublicKey,
		PrivilegePublicKey:  p.PrivilegePublicKey,
		DisplayName:         p.DisplayName,
		Platform:            p.Platform,
		Level:               int(p.Level),
		GrantedCapabilities: p.GrantedCapabilities,
		AuthPolicy:          p.AuthPolicy,
		TokenGrantedAt:      p.TokenGrantedAt,
		ProtectionTier:      int(p.ProtectionTier),
		CreatedAt:           p.CreatedAt,
		LastSeen:            p.LastSeen,
	}
}

func (s storedPeer) toPeer() Peer {
	return Peer{
		DeviceID:            s.DeviceID,
		IdentityPublicKey:   ed25519.PublicKey(s.IdentityPublicKey),
		PrivilegePublicKey:  ed25519.PublicKey(s.PrivilegePublicKey),
		DisplayName:         s.DisplayName,
		Platform:            s.Platform,
		Level:               TrustLevel(s.Level),
		GrantedCapabilities: s.GrantedCapabilities,
		AuthPolicy:          s.AuthPolicy,
		TokenGrantedAt:      s.TokenGrantedAt,
		ProtectionTier:      ProtectionTier(s.ProtectionTier),
		CreatedAt:           s.CreatedAt,
		LastSeen:            s.LastSeen,
	}
}

// OpenTrustStore loads the store at path, starting empty if the file does not
// exist yet. M1 has no pairing, so empty is the normal case.
func OpenTrustStore(path string) (*FileTrustStore, error) {
	if path == "" {
		return nil, errors.New("identity: trust store path is required")
	}
	s := &FileTrustStore{path: path, peers: make(map[DeviceID]Peer)}

	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, fmt.Errorf("identity: read trust store %s: %w", path, err)
	}
	var f trustStoreFile
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, fmt.Errorf("identity: parse trust store %s: %w", path, err)
	}
	if f.Version != trustStoreVersion {
		return nil, fmt.Errorf("identity: trust store %s is version %d, want %d", path, f.Version, trustStoreVersion)
	}
	for _, sp := range f.Peers {
		p := sp.toPeer()
		if err := validatePeer(p); err != nil {
			return nil, fmt.Errorf("identity: trust store %s: %w", path, err)
		}
		s.peers[p.DeviceID] = p
	}
	return s, nil
}

// Get returns a copy of the peer record, and whether it was present.
func (s *FileTrustStore) Get(id DeviceID) (Peer, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	p, ok := s.peers[id]
	if !ok {
		return Peer{}, false
	}
	return clonePeer(p), true
}

// List returns copies of every peer, ordered by DeviceID so that callers -- and
// the persisted file -- see a stable order rather than Go's map iteration.
func (s *FileTrustStore) List() []Peer {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Peer, 0, len(s.peers))
	for _, p := range s.peers {
		out = append(out, clonePeer(p))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].DeviceID < out[j].DeviceID })
	return out
}

// Put inserts or replaces a peer record and persists the store.
//
// The DeviceID is checked against the identity key it claims to summarise
// (PROTOCOL.md §2). A record whose ID does not derive from its own pinned key
// would make every later lookup a lie, and pairing is the one place that can
// still catch it cheaply.
func (s *FileTrustStore) Put(p Peer) error {
	if err := validatePeer(p); err != nil {
		return err
	}
	stored := clonePeer(p)

	s.mu.Lock()
	defer s.mu.Unlock()
	prev, existed := s.peers[p.DeviceID]
	s.peers[p.DeviceID] = stored
	if err := s.flushLocked(); err != nil {
		if existed {
			s.peers[p.DeviceID] = prev
		} else {
			delete(s.peers, p.DeviceID)
		}
		return err
	}
	return nil
}

// Delete removes a peer -- a full unpair, as distinct from the demotion to
// Trusted that D-20 also requires. Reports ErrUnknownPeer if absent.
func (s *FileTrustStore) Delete(id DeviceID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	prev, ok := s.peers[id]
	if !ok {
		return fmt.Errorf("%w: %s", ErrUnknownPeer, id)
	}
	delete(s.peers, id)
	if err := s.flushLocked(); err != nil {
		s.peers[id] = prev
		return err
	}
	return nil
}

// flushLocked rewrites the whole file. Caller holds the write lock.
func (s *FileTrustStore) flushLocked() error {
	f := trustStoreFile{Version: trustStoreVersion, Peers: make([]storedPeer, 0, len(s.peers))}
	ids := make([]DeviceID, 0, len(s.peers))
	for id := range s.peers {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	for _, id := range ids {
		f.Peers = append(f.Peers, toStored(s.peers[id]))
	}
	b, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return fmt.Errorf("identity: encode trust store: %w", err)
	}
	return writeFileAtomic(s.path, b, keyFileMode)
}

func validatePeer(p Peer) error {
	if !p.DeviceID.Valid() {
		return fmt.Errorf("identity: peer DeviceID %q is not 16 lowercase base32 characters", p.DeviceID)
	}
	if len(p.IdentityPublicKey) != ed25519.PublicKeySize {
		return fmt.Errorf("identity: peer %s identity key is %d bytes, want %d",
			p.DeviceID, len(p.IdentityPublicKey), ed25519.PublicKeySize)
	}
	if got := DeriveDeviceID(p.IdentityPublicKey); got != p.DeviceID {
		return fmt.Errorf("identity: peer DeviceID %s does not derive from its identity key (got %s)",
			p.DeviceID, got)
	}
	if n := len(p.PrivilegePublicKey); n != 0 && n != ed25519.PublicKeySize {
		return fmt.Errorf("identity: peer %s privilege key is %d bytes, want %d or none",
			p.DeviceID, n, ed25519.PublicKeySize)
	}
	// D-21 tier 3: a device that does not protect a privilege key holds none,
	// and must never be granted Owned.
	if p.Level == LevelOwned && (p.ProtectionTier == TierNone || len(p.PrivilegePublicKey) == 0) {
		return fmt.Errorf("identity: peer %s is Owned but has protection tier %d and %d-byte privilege key",
			p.DeviceID, p.ProtectionTier, len(p.PrivilegePublicKey))
	}
	return nil
}

func clonePeer(p Peer) Peer {
	out := p
	out.IdentityPublicKey = cloneBytes(p.IdentityPublicKey)
	out.PrivilegePublicKey = cloneBytes(p.PrivilegePublicKey)
	out.GrantedCapabilities = cloneBytes(p.GrantedCapabilities)
	return out
}

func cloneBytes[T ~[]byte](b T) T {
	if b == nil {
		return nil
	}
	out := make(T, len(b))
	copy(out, b)
	return out
}

package remotefs

import (
	"container/list"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"sync"

	"golang.org/x/crypto/chacha20poly1305"

	"github.com/shreyashsri79/openair/internal/identity"
)

// The client-side block cache, §11.4 and PRD K8.
//
// §11.4 makes this optional and then attaches two conditions to it that are not
// optional at all: a cache that holds someone else's files MUST be encrypted at
// rest and MUST be size-capped. Both follow from what it is — this is the one
// place where a device that browsed another device's photos keeps copies of
// them, on disk, without anybody deciding to save anything.
//
// # Why the key is ephemeral
//
// The key is random per process and never written down, so the cache is
// readable only by the process that wrote it and only while it is running. The
// alternative — deriving it from the device key so the cache survives a restart
// — buys a warm cache after a reboot and costs a durable, decryptable copy of
// another device's files sitting in a directory forever. A streaming cache is
// worth very little cold, so the trade is easy: NewCache wipes the directory on
// start, and everything in it before that moment is unreadable noise anyway
// (D-78).
//
// # Why on disk at all
//
// Because the point is files larger than memory. A 40 GB film seeked around in
// for an hour is what this is for, and holding its working set in the heap is
// how a daemon gets killed.

const (
	// DefaultCacheBytes is the cap when the caller does not choose one. 512 MiB
	// is a comfortable working set for one film and small enough to sit in a
	// cache directory without anyone minding.
	DefaultCacheBytes = 512 << 20

	// cacheFileMode and cacheDirMode keep the cache owner-only. The contents are
	// encrypted, but a file nobody else can open is one fewer thing to argue
	// about.
	cacheFileMode = 0o600
	cacheDirMode  = 0o700
)

// Cache is a size-capped, encrypted, least-recently-used cache of file blocks.
//
// The zero value is not usable; use NewCache. A nil *Cache is, deliberately:
// every method tolerates it, so a caller that has no cache configured passes
// nil rather than branching.
type Cache struct {
	dir  string
	max  int64
	aead interface {
		Seal(dst, nonce, plaintext, additionalData []byte) []byte
		Open(dst, nonce, ciphertext, additionalData []byte) ([]byte, error)
		NonceSize() int
	}

	mu      sync.Mutex
	lru     *list.List               // front is most recently used
	entries map[string]*list.Element // file name to element
	size    int64
}

type cacheEntry struct {
	name  string
	bytes int64
}

// NewCache prepares a cache directory. Anything already there is removed: it
// was written under a key this process does not have.
func NewCache(dir string, maxBytes int64) (*Cache, error) {
	if maxBytes <= 0 {
		maxBytes = DefaultCacheBytes
	}
	if err := os.RemoveAll(dir); err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	if err := os.MkdirAll(dir, cacheDirMode); err != nil {
		return nil, err
	}

	key := make([]byte, chacha20poly1305.KeySize)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return nil, err
	}

	return &Cache{
		dir:     dir,
		max:     maxBytes,
		aead:    aead,
		lru:     list.New(),
		entries: make(map[string]*list.Element),
	}, nil
}

// Get returns a cached block.
func (c *Cache) Get(key string, idx uint64) ([]byte, bool) {
	if c == nil {
		return nil, false
	}
	name := blockName(key, idx)

	c.mu.Lock()
	el, ok := c.entries[name]
	if ok {
		c.lru.MoveToFront(el)
	}
	c.mu.Unlock()
	if !ok {
		return nil, false
	}

	sealed, err := os.ReadFile(filepath.Join(c.dir, name))
	if err != nil {
		c.drop(name)
		return nil, false
	}
	ns := c.aead.NonceSize()
	if len(sealed) < ns {
		c.drop(name)
		return nil, false
	}
	// The file name is the associated data, so a block moved to another file
	// name -- another path, another offset -- does not open.
	plain, err := c.aead.Open(nil, sealed[:ns], sealed[ns:], []byte(name))
	if err != nil {
		c.drop(name)
		return nil, false
	}
	return plain, true
}

// Put stores a block, evicting least-recently-used blocks to stay under the
// cap. A failure to write is not an error the caller needs: a cache that cannot
// store is a cache that misses.
func (c *Cache) Put(key string, idx uint64, data []byte) {
	if c == nil || len(data) == 0 {
		return
	}
	name := blockName(key, idx)

	nonce := make([]byte, c.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return
	}
	sealed := append(nonce, c.aead.Seal(nil, nonce, data, []byte(name))...)

	// Written to a temporary name and renamed, so a reader never sees a block
	// that is half written.
	tmp, err := os.CreateTemp(c.dir, "part-")
	if err != nil {
		return
	}
	if _, err := tmp.Write(sealed); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return
	}
	if err := os.Chmod(tmp.Name(), cacheFileMode); err != nil {
		os.Remove(tmp.Name())
		return
	}
	if err := os.Rename(tmp.Name(), filepath.Join(c.dir, name)); err != nil {
		os.Remove(tmp.Name())
		return
	}

	c.mu.Lock()
	if el, ok := c.entries[name]; ok {
		c.size -= el.Value.(*cacheEntry).bytes
		c.lru.Remove(el)
	}
	c.entries[name] = c.lru.PushFront(&cacheEntry{name: name, bytes: int64(len(sealed))})
	c.size += int64(len(sealed))
	victims := c.evictLocked()
	c.mu.Unlock()

	for _, v := range victims {
		os.Remove(filepath.Join(c.dir, v))
	}
}

// Bytes is how much the cache currently holds on disk.
func (c *Cache) Bytes() int64 {
	if c == nil {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.size
}

// Close removes the cache directory. The contents are unreadable without the
// in-memory key, but leaving a pile of ciphertext behind is still litter.
func (c *Cache) Close() error {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	c.entries = make(map[string]*list.Element)
	c.lru.Init()
	c.size = 0
	c.mu.Unlock()
	return os.RemoveAll(c.dir)
}

// evictLocked drops least-recently-used entries until the cache is under its
// cap, returning the file names to unlink. c.mu must be held.
func (c *Cache) evictLocked() []string {
	var victims []string
	for c.size > c.max {
		el := c.lru.Back()
		if el == nil {
			break
		}
		ent := el.Value.(*cacheEntry)
		c.lru.Remove(el)
		delete(c.entries, ent.name)
		c.size -= ent.bytes
		victims = append(victims, ent.name)
	}
	return victims
}

// drop forgets a block whose file is missing or will not open.
func (c *Cache) drop(name string) {
	c.mu.Lock()
	if el, ok := c.entries[name]; ok {
		c.size -= el.Value.(*cacheEntry).bytes
		c.lru.Remove(el)
		delete(c.entries, name)
	}
	c.mu.Unlock()
	os.Remove(filepath.Join(c.dir, name))
}

// cacheKey identifies one version of one remote file.
//
// Size and modification time are in it so that a file replaced on the source
// gets a different key rather than a cache full of the previous version's
// blocks. The device ID is in it because two devices may share a path that
// names entirely different files.
func cacheKey(device identity.DeviceID, path string, size uint64, modifiedAt int64) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%s\x00%d\x00%d", device, path, size, modifiedAt)))
	return hex.EncodeToString(sum[:16])
}

func blockName(key string, idx uint64) string {
	return key + "-" + strconv.FormatUint(idx, 10)
}

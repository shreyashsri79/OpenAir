package files

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"sync"

	openairv1 "github.com/shreyashsri79/openair/internal/wire/openair/v1"
)

// PartSuffix marks an incomplete destination file. Data lands in `name.oapart`
// and is renamed to `name` only once every chunk has verified, so a cancelled
// or crashed transfer can never present a partial file as complete (§8.5).
const PartSuffix = ".oapart"

// stateMagic identifies a resume file. The header binds the bitmap to the offer
// it came from: reusing a transfer id with a different file list must not
// resurrect the wrong verified-chunk set.
var stateMagic = [8]byte{'O', 'A', 'P', 'A', 'R', 'T', 0, 1}

const stateHeaderSize = 8 + 8 + 8 + 32 // magic, chunkSize, chunkCount, offerHash

// offerHash binds a resume bitmap to the offer that produced it.
func offerHash(metas []*openairv1.FileMeta, chunkSize uint64) [32]byte {
	h := sha256.New()
	var n [8]byte
	binary.LittleEndian.PutUint64(n[:], chunkSize)
	h.Write(n[:])
	for _, m := range metas {
		h.Write([]byte(m.GetPath()))
		h.Write([]byte{0})
		binary.LittleEndian.PutUint64(n[:], m.GetSize())
		h.Write(n[:])
	}
	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out
}

// chunkSet is the receiver's verified-chunk bitmap, persisted so that resume
// survives a reconnect rather than only a retry inside one process (§8.4).
type chunkSet struct {
	mu    sync.Mutex
	bits  []byte
	count uint64 // chunks marked
	n     uint64 // chunks total

	path  string
	hdr   [stateHeaderSize]byte
	dirty bool
}

func newChunkSet(dir, transferID string, n, chunkSize uint64, oh [32]byte) *chunkSet {
	s := &chunkSet{
		bits: make([]byte, (n+7)/8),
		n:    n,
		path: filepath.Join(dir, statePathFor(transferID)),
	}
	copy(s.hdr[0:8], stateMagic[:])
	binary.LittleEndian.PutUint64(s.hdr[8:16], chunkSize)
	binary.LittleEndian.PutUint64(s.hdr[16:24], n)
	copy(s.hdr[24:56], oh[:])
	return s
}

// statePathFor keeps the state file name filesystem-safe whatever the peer put
// in transfer_id: the id is attacker-controlled and must not become a path.
func statePathFor(transferID string) string {
	sum := sha256.Sum256([]byte(transferID))
	return "t" + strconv.FormatUint(binary.LittleEndian.Uint64(sum[:8]), 16) + ".state"
}

// load reads a previous run's bitmap, if one exists and matches this offer.
// A mismatch is not an error: it just means there is nothing to resume.
func (s *chunkSet) load() {
	b, err := os.ReadFile(s.path)
	if err != nil || len(b) < stateHeaderSize {
		return
	}
	if !bytes.Equal(b[:stateHeaderSize], s.hdr[:]) {
		return
	}
	body := b[stateHeaderSize:]
	if uint64(len(body)) != uint64(len(s.bits)) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	copy(s.bits, body)
	s.count = 0
	for i := uint64(0); i < s.n; i++ {
		if s.bits[i/8]&(1<<(i%8)) != 0 {
			s.count++
		}
	}
}

func (s *chunkSet) has(i uint64) bool {
	if i >= s.n {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.bits[i/8]&(1<<(i%8)) != 0
}

// mark records chunk i as verified and reports the new total.
func (s *chunkSet) mark(i uint64) uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	if i < s.n && s.bits[i/8]&(1<<(i%8)) == 0 {
		s.bits[i/8] |= 1 << (i % 8)
		s.count++
		s.dirty = true
	}
	return s.count
}

func (s *chunkSet) marked() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.count
}

func (s *chunkSet) complete() bool { return s.marked() == s.n }

// list returns the verified indices, for TransferAccept.have_chunks.
func (s *chunkSet) list() []uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.count == 0 {
		return nil
	}
	out := make([]uint64, 0, s.count)
	for i := uint64(0); i < s.n; i++ {
		if s.bits[i/8]&(1<<(i%8)) != 0 {
			out = append(out, i)
		}
	}
	return out
}

// flush persists the bitmap. Written whole rather than incrementally: at one
// bit per chunk the file is small, and a torn incremental update would be
// worse than a slightly stale one.
func (s *chunkSet) flush() error {
	s.mu.Lock()
	if !s.dirty {
		s.mu.Unlock()
		return nil
	}
	buf := make([]byte, 0, stateHeaderSize+len(s.bits))
	buf = append(buf, s.hdr[:]...)
	buf = append(buf, s.bits...)
	s.dirty = false
	s.mu.Unlock()

	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, buf, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func (s *chunkSet) discard() error {
	err := os.Remove(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func (s *chunkSet) String() string {
	return fmt.Sprintf("chunkSet{%d/%d}", s.marked(), s.n)
}

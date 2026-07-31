package mobile

import (
	"os"
	"path/filepath"
	"sync"

	"github.com/shreyashsri79/openair/internal/caps/files"
	"github.com/shreyashsri79/openair/internal/identity"
	openairv1 "github.com/shreyashsri79/openair/internal/wire/openair/v1"
)

// PeerInfo describes the device on the other end of a session, as learned from
// its Hello (PROTOCOL.md §4). Everything in it is self-reported except
// Fingerprint, which is derived from the key that terminated TLS and is
// therefore the only field worth trusting before pairing exists.
type PeerInfo struct {
	deviceID    string
	displayName string
	platform    string
	trusted     bool
}

func newPeerInfo(p identity.Peer) *PeerInfo {
	return &PeerInfo{
		deviceID:    string(p.DeviceID),
		displayName: p.DisplayName,
		platform:    p.Platform,
		trusted:     p.Level >= identity.LevelTrusted,
	}
}

// DeviceID is the peer's identifier, unformatted.
func (p *PeerInfo) DeviceID() string { return p.deviceID }

// Fingerprint is DeviceID grouped for display. This is the string the user
// compares against the other device's screen.
func (p *PeerInfo) Fingerprint() string { return FormatFingerprint(p.deviceID) }

// DisplayName is the peer's self-reported name. It is not authenticated: a
// hostile peer picks whatever name it likes, so show it beside the fingerprint,
// never instead of it.
func (p *PeerInfo) DisplayName() string { return p.displayName }

// Platform is the peer's self-reported platform, one of the four values in
// PROTOCOL.md §4. Also unauthenticated.
func (p *PeerInfo) Platform() string { return p.platform }

// Trusted reports whether this peer is already at Trusted level or above. It is
// always false until pairing lands (M2); a shell can key "known device" UI off
// it now and get the right behaviour later without changing.
func (p *PeerInfo) Trusted() bool { return p.trusted }

// Offer is an inbound transfer offer awaiting a decision (PROTOCOL.md §8.1).
//
// The file list is exposed by index rather than as a slice because gobind
// cannot carry a slice of structs across the boundary.
type Offer struct {
	transferID string
	metas      []*openairv1.FileMeta
	totalBytes int64
}

func newOffer(o files.Offer) *Offer {
	return &Offer{
		transferID: o.TransferID,
		metas:      o.Files,
		totalBytes: int64(o.TotalBytes),
	}
}

// TransferID identifies this transfer for its whole lifetime, including across
// a resume.
func (o *Offer) TransferID() string { return o.transferID }

// FileCount is how many files the offer contains.
func (o *Offer) FileCount() int { return len(o.metas) }

// TotalBytes is the offer's total size.
func (o *Offer) TotalBytes() int64 { return o.totalBytes }

// Path returns the relative path of file i, or "" if i is out of range.
//
// It is the path the sender asked for, not where the file will land: the
// receiver resolves it under the destination root and refuses anything that
// escapes (§8.1). Display it, do not resolve it.
func (o *Offer) Path(i int) string {
	if i < 0 || i >= len(o.metas) {
		return ""
	}
	return o.metas[i].GetPath()
}

// Size returns the size of file i, or -1 if i is out of range.
func (o *Offer) Size(i int) int64 {
	if i < 0 || i >= len(o.metas) {
		return -1
	}
	return int64(o.metas[i].GetSize())
}

// FileList is an ordered set of local files to send. It replaces the []Item a
// Go caller would pass, which gobind cannot express.
//
// A FileList is safe for concurrent use, because a Compose UI mutates it from
// the main thread while the send goroutine reads it.
type FileList struct {
	mu    sync.Mutex
	items []files.Item
	sizes []int64
}

// NewFileList returns an empty list.
func NewFileList() *FileList { return &FileList{} }

// Add appends a local file.
//
// relPath is the name the receiver sees; empty means the base name of
// localPath. It must stay a relative path — an absolute one, or one climbing
// out with "..", is refused here rather than left for the receiver to catch,
// so a bad path fails at the point the user can still fix it.
//
// The file is stat'd now, so a path that does not exist or is a directory fails
// at attach time instead of halfway through a transfer. Directory trees are an
// offer of many FileMeta and the walk is not implemented yet, matching the CLI.
func (l *FileList) Add(localPath string, relPath string) error {
	st, err := os.Stat(localPath)
	if err != nil {
		return err
	}
	if st.IsDir() {
		return &pathError{op: "add", path: localPath, msg: "is a directory; directory transfer is not implemented yet"}
	}
	if relPath == "" {
		relPath = filepath.Base(localPath)
	}
	if filepath.IsAbs(relPath) || !filepath.IsLocal(relPath) {
		return &pathError{op: "add", path: relPath, msg: "must be a relative path that stays inside the destination"}
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	l.items = append(l.items, files.Item{LocalPath: localPath, RelPath: filepath.ToSlash(relPath)})
	l.sizes = append(l.sizes, st.Size())
	return nil
}

// Len is how many files are queued.
func (l *FileList) Len() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.items)
}

// Path returns the local path of entry i, or "" if i is out of range.
func (l *FileList) Path(i int) string {
	l.mu.Lock()
	defer l.mu.Unlock()
	if i < 0 || i >= len(l.items) {
		return ""
	}
	return l.items[i].LocalPath
}

// Name returns the relative path entry i will be written under, or "" if i is
// out of range.
func (l *FileList) Name(i int) string {
	l.mu.Lock()
	defer l.mu.Unlock()
	if i < 0 || i >= len(l.items) {
		return ""
	}
	return l.items[i].RelPath
}

// Size returns the size of entry i as measured at Add time, or -1 if i is out
// of range.
func (l *FileList) Size(i int) int64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	if i < 0 || i >= len(l.sizes) {
		return -1
	}
	return l.sizes[i]
}

// TotalBytes is the sum of every entry's size.
func (l *FileList) TotalBytes() int64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	var n int64
	for _, s := range l.sizes {
		n += s
	}
	return n
}

// RemoveAt drops entry i. Out-of-range indices are ignored, because the UI
// removing a chip it has already removed is a race, not a fault.
func (l *FileList) RemoveAt(i int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if i < 0 || i >= len(l.items) {
		return
	}
	l.items = append(l.items[:i], l.items[i+1:]...)
	l.sizes = append(l.sizes[:i], l.sizes[i+1:]...)
}

// Clear empties the list.
func (l *FileList) Clear() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.items, l.sizes = nil, nil
}

// snapshot copies the items so a send is not affected by the UI mutating the
// list mid-transfer.
func (l *FileList) snapshot() []files.Item {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]files.Item, len(l.items))
	copy(out, l.items)
	return out
}

// pathError is a small local error type; gobind renders any error as an
// Exception carrying its message, so the shape does not cross the boundary.
type pathError struct {
	op   string
	path string
	msg  string
}

func (e *pathError) Error() string { return e.op + " " + e.path + ": " + e.msg }

// Package remotefs is PROTOCOL.md §11: browsing and reading another device's
// files without transferring them (capID 3, PRD R14–R16).
//
// The shape is the one §11 insists on and it is worth stating before any code:
// a dumb server and a smart client. The source lists directories, stats paths
// and serves byte ranges. It does not know it is serving a video, does not
// read ahead, and keeps no per-client state at all — every request is answered
// from the path it names. Read-ahead, buffering and caching belong to the
// client, which is the only side that knows what it is doing with the bytes
// (§11.4, M11).
//
// Two rules make it safe to point at a filesystem.
//
// Roots are configured on the source. A wire path names a root and a path
// inside it, and anything that would leave that root is refused with
// UNAUTHORISED (§11.1) — checked syntactically before any filesystem call, and
// again after resolution, the same two-step internal/caps/files uses for a
// transfer destination.
//
// It requires Owned. Reading a device's files unattended is the operation §6
// exists for: the trust store says the peer may, and an AuthProof says a human
// authenticated on the peer within the last six hours (D-18). Because every
// request is its own stream, that proof travels on the stream it authorises
// rather than the control stream — see session.OpenOwnedStream and D-57.
package remotefs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"google.golang.org/protobuf/proto"

	"github.com/shreyashsri79/openair/internal/identity"
	"github.com/shreyashsri79/openair/internal/session"
	openairv1 "github.com/shreyashsri79/openair/internal/wire/openair/v1"
)

// CapID is the remotefs capability's wire ID (Appendix B).
const CapID byte = 0x03

// Message types (§11). Like every other *MessageType enum these are the wire
// values with 0 invalid (D-39); no offset applies.
const (
	MsgStatRequest   = uint16(openairv1.RemotefsMessageType_REMOTEFS_MESSAGE_TYPE_STAT_REQUEST)
	MsgStatResponse  = uint16(openairv1.RemotefsMessageType_REMOTEFS_MESSAGE_TYPE_STAT_RESPONSE)
	MsgListRequest   = uint16(openairv1.RemotefsMessageType_REMOTEFS_MESSAGE_TYPE_LIST_REQUEST)
	MsgListResponse  = uint16(openairv1.RemotefsMessageType_REMOTEFS_MESSAGE_TYPE_LIST_RESPONSE)
	MsgReadRequest   = uint16(openairv1.RemotefsMessageType_REMOTEFS_MESSAGE_TYPE_READ_REQUEST)
	MsgReadResponse  = uint16(openairv1.RemotefsMessageType_REMOTEFS_MESSAGE_TYPE_READ_RESPONSE)
	MsgThumbRequest  = uint16(openairv1.RemotefsMessageType_REMOTEFS_MESSAGE_TYPE_THUMB_REQUEST)
	MsgThumbResponse = uint16(openairv1.RemotefsMessageType_REMOTEFS_MESSAGE_TYPE_THUMB_RESPONSE)
)

const (
	// DefaultListLimit is how many entries one ListResponse carries when the
	// client does not say. §11.1 makes listing paginated precisely so that a
	// directory of 100k entries is not one 16 MiB envelope.
	DefaultListLimit = 256

	// MaxListLimit bounds what a client may ask for in one response. A
	// FileStat is well under a kilobyte, so a thousand of them is a comfortable
	// envelope and a hundred thousand is not.
	MaxListLimit = 1024

	// MaxReadLength is the largest range one read may return. It is the
	// "smaller quantum" §11.2 allows a source to choose, and a client that asks
	// for more gets this much and asks again -- which is why §11.2 requires
	// clients to handle short reads.
	MaxReadLength = 1 << 20

	// sniffBytes is how much of a file is read to identify its type when the
	// extension does not. http.DetectContentType looks at 512.
	sniffBytes = 512
)

// ErrNotShared reports a path outside every configured root, or a root that
// does not exist. It maps to UNAUTHORISED (§11.1).
var ErrNotShared = errors.New("remotefs: that path is not shared")

// Root is one directory this device offers for browsing.
type Root struct {
	// Name is what the peer sees as the first path component. Empty means the
	// base name of Path.
	Name string

	// Path is the local directory. It is resolved once, at New, so a symlink
	// swapped underneath later cannot move the root.
	Path string
}

// Config configures the capability.
//
// The zero value shares nothing, which is the right default: a device that has
// not been told what to offer offers nothing, and every request is refused.
type Config struct {
	// Roots are the directories exposed for browsing (§11.1).
	Roots []Root

	// MaxRead overrides the largest range served in one response. Zero means
	// MaxReadLength.
	MaxRead int

	// Thumbnails enables §11.3. It is off unless asked for, because generating
	// one costs the source real work on a file it was only asked to list.
	Thumbnails bool

	Logf func(format string, args ...any)
}

// Capability is the remotefs source (§11).
type Capability struct {
	cfg   Config
	roots []resolvedRoot
	thumb *thumbCache
}

type resolvedRoot struct {
	name string
	dir  string
}

// New builds the capability, resolving every root once.
//
// A root that cannot be resolved is dropped with a log line rather than
// failing: one bad path in a configuration should cost that share, not the
// daemon's ability to start.
func New(cfg Config) *Capability {
	if cfg.Logf == nil {
		cfg.Logf = func(string, ...any) {}
	}
	c := &Capability{cfg: cfg, thumb: newThumbCache()}
	seen := map[string]struct{}{}
	for _, r := range cfg.Roots {
		abs, err := filepath.Abs(r.Path)
		if err != nil {
			cfg.Logf("not sharing %s: %v", r.Path, err)
			continue
		}
		abs, err = filepath.EvalSymlinks(abs)
		if err != nil {
			cfg.Logf("not sharing %s: %v", r.Path, err)
			continue
		}
		name := r.Name
		if name == "" {
			name = filepath.Base(abs)
		}
		if _, dup := seen[name]; dup {
			cfg.Logf("not sharing %s: another root is already called %q", r.Path, name)
			continue
		}
		seen[name] = struct{}{}
		c.roots = append(c.roots, resolvedRoot{name: name, dir: abs})
	}
	sort.Slice(c.roots, func(i, j int) bool { return c.roots[i].name < c.roots[j].name })
	return c
}

func (c *Capability) CapID() byte { return CapID }

// RequiredLevel is Owned. See the package comment: browsing a device's
// filesystem while nobody is watching it is the operation §6 exists to gate.
func (c *Capability) RequiredLevel() identity.TrustLevel { return identity.LevelOwned }

// Roots reports what this device shares, for a status line.
func (c *Capability) Roots() []string {
	out := make([]string, 0, len(c.roots))
	for _, r := range c.roots {
		out = append(out, fmt.Sprintf("%s (%s)", r.name, r.dir))
	}
	return out
}

// Serve refuses: every remotefs operation is a stream (§11.2, and D-71 for why
// the metadata ones are too).
func (c *Capability) Serve(context.Context, session.Session, uint16, []byte) error {
	return session.ErrUnknownMsgType
}

// ServeStream answers one request. The stream carries exactly one operation,
// which is what lets several reads proceed without head-of-line blocking
// between them (§11.2).
func (c *Capability) ServeStream(ctx context.Context, sess session.Session, st session.Stream, msgType uint16, payload []byte) error {
	var err error
	switch msgType {
	case MsgStatRequest:
		err = c.serveStat(st, payload)
	case MsgListRequest:
		err = c.serveList(st, payload)
	case MsgReadRequest:
		err = c.serveRead(st, payload)
	case MsgThumbRequest:
		err = c.serveThumb(ctx, st, payload)
	default:
		err = session.ErrUnknownMsgType
	}
	if err != nil {
		// Deliberately *not* closed here. Closing ends the stream cleanly, and
		// the session layer resets it with the §10 code immediately afterwards
		// -- so the client would see the clean end first and read the refusal
		// as an unexplained EOF. The reset is the message.
		return err
	}
	return st.Close()
}

func (c *Capability) serveStat(st session.Stream, payload []byte) error {
	var req openairv1.StatRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		return protoErr(session.CodeProtocolViolation, err, "malformed StatRequest")
	}
	stat, err := c.stat(req.GetPath())
	if err != nil {
		return err
	}
	return writeMessage(st, MsgStatResponse, &openairv1.StatResponse{Stat: stat})
}

func (c *Capability) serveList(st session.Stream, payload []byte) error {
	var req openairv1.ListRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		return protoErr(session.CodeProtocolViolation, err, "malformed ListRequest")
	}
	entries, truncated, err := c.list(req.GetPath(), int(req.GetOffset()), int(req.GetLimit()))
	if err != nil {
		return err
	}
	return writeMessage(st, MsgListResponse, &openairv1.ListResponse{
		Entries:   entries,
		Truncated: truncated,
	})
}

// serveRead is §11.2: one ReadResponse envelope, then raw bytes.
func (c *Capability) serveRead(st session.Stream, payload []byte) error {
	var req openairv1.ReadRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		return protoErr(session.CodeProtocolViolation, err, "malformed ReadRequest")
	}
	full, err := c.resolve(req.GetPath())
	if err != nil {
		return err
	}

	f, err := os.Open(full)
	if err != nil {
		return fsErr(req.GetPath(), err)
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return fsErr(req.GetPath(), err)
	}
	if info.IsDir() {
		return protoErr(session.CodeProtocolViolation, nil, "%s is a directory", req.GetPath())
	}

	offset := req.GetOffset()
	size := uint64(info.Size())
	if offset > size {
		// Past the end is not an error: it is a client that seeked beyond what
		// the file was when it looked. An empty response with eof says so.
		return writeMessage(st, MsgReadResponse, &openairv1.ReadResponse{Offset: offset, Length: 0, Eof: true})
	}

	length := req.GetLength()
	if max := uint64(c.maxRead()); length == 0 || length > max {
		length = max
	}
	if remaining := size - offset; length > remaining {
		length = remaining
	}

	if err := writeMessage(st, MsgReadResponse, &openairv1.ReadResponse{
		Offset: offset,
		Length: length,
		Eof:    offset+length >= size,
	}); err != nil {
		return err
	}
	if length == 0 {
		return nil
	}
	if _, err := io.Copy(st, io.NewSectionReader(f, int64(offset), int64(length))); err != nil {
		return fmt.Errorf("remotefs: serving %s: %w", req.GetPath(), err)
	}
	return nil
}

func (c *Capability) maxRead() int {
	if c.cfg.MaxRead > 0 && c.cfg.MaxRead <= MaxReadLength {
		return c.cfg.MaxRead
	}
	return MaxReadLength
}

// stat answers one StatRequest. The empty path is the share list itself.
func (c *Capability) stat(wirePath string) (*openairv1.FileStat, error) {
	if isRootPath(wirePath) {
		return &openairv1.FileStat{Path: "", IsDir: true, Mode: 0o555}, nil
	}
	full, err := c.resolve(wirePath)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(full)
	if err != nil {
		return nil, fsErr(wirePath, err)
	}
	return c.fileStat(cleanWirePath(wirePath), full, info, true), nil
}

// list answers one ListRequest, paginated (§11.1).
func (c *Capability) list(wirePath string, offset, limit int) ([]*openairv1.FileStat, bool, error) {
	if limit <= 0 || limit > MaxListLimit {
		if limit <= 0 {
			limit = DefaultListLimit
		} else {
			limit = MaxListLimit
		}
	}
	if offset < 0 {
		offset = 0
	}

	// The empty path lists the roots. Without it a client has no way to find
	// out what it may browse, and would have to be told out of band -- which
	// for a capability whose whole purpose is browsing would be an odd hole.
	if isRootPath(wirePath) {
		entries := make([]*openairv1.FileStat, 0, len(c.roots))
		for _, r := range c.roots {
			info, err := os.Stat(r.dir)
			if err != nil {
				continue
			}
			entries = append(entries, c.fileStat(r.name, r.dir, info, false))
		}
		return page(entries, offset, limit)
	}

	full, err := c.resolve(wirePath)
	if err != nil {
		return nil, false, err
	}
	dir, err := os.Open(full)
	if err != nil {
		return nil, false, fsErr(wirePath, err)
	}
	defer dir.Close()

	names, err := dir.Readdirnames(-1)
	if err != nil {
		return nil, false, fsErr(wirePath, err)
	}
	// Sorted, so that paging through a directory twice sees the same order.
	// Readdir order is the filesystem's and is not stable across calls, which
	// would make an offset mean nothing.
	sort.Strings(names)

	base := cleanWirePath(wirePath)
	entries := make([]*openairv1.FileStat, 0, min(len(names), limit))
	end := min(offset+limit, len(names))
	for i := offset; i < end && i < len(names); i++ {
		child := filepath.Join(full, names[i])
		info, err := os.Lstat(child)
		if err != nil {
			continue
		}
		if info.Mode()&os.ModeSymlink != 0 {
			// A symlink is followed only if it stays inside the root, which
			// resolve checks. One that escapes is simply not listed: naming it
			// would advertise a path the source will refuse to open.
			if _, err := c.resolve(path.Join(base, names[i])); err != nil {
				continue
			}
			if info, err = os.Stat(child); err != nil {
				continue
			}
		}
		entries = append(entries, c.fileStat(path.Join(base, names[i]), child, info, false))
	}
	return entries, end < len(names), nil
}

func page(entries []*openairv1.FileStat, offset, limit int) ([]*openairv1.FileStat, bool, error) {
	if offset >= len(entries) {
		return nil, false, nil
	}
	end := min(offset+limit, len(entries))
	return entries[offset:end], end < len(entries), nil
}

// fileStat fills in §11.1's FileStat. sniff says whether the source may read
// the head of the file to identify it -- true for a single stat, false while
// listing a directory, where it would mean opening every file in it.
func (c *Capability) fileStat(wirePath, full string, info os.FileInfo, sniff bool) *openairv1.FileStat {
	stat := &openairv1.FileStat{
		Path:       wirePath,
		Size:       uint64(info.Size()),
		ModifiedAt: info.ModTime().UnixMilli(),
		IsDir:      info.IsDir(),
		Mode:       uint32(info.Mode().Perm()),
	}
	if info.IsDir() {
		return stat
	}
	stat.Mime = mime.TypeByExtension(filepath.Ext(full))
	if stat.Mime == "" && sniff {
		stat.Mime = sniffType(full)
	}
	return stat
}

func sniffType(full string) string {
	f, err := os.Open(full)
	if err != nil {
		return ""
	}
	defer f.Close()
	buf := make([]byte, sniffBytes)
	n, _ := io.ReadFull(f, buf)
	if n == 0 {
		return ""
	}
	return http.DetectContentType(buf[:n])
}

// resolve turns a wire path into a local one, or refuses it.
//
// Three checks, and each catches something the others do not: the syntax check
// rejects traversal before any filesystem call, the root lookup rejects a share
// that does not exist, and the post-resolution prefix check catches a symlink
// that points out of the root -- which the syntax cannot see.
func (c *Capability) resolve(wirePath string) (string, error) {
	rel, err := safeWirePath(wirePath)
	if err != nil {
		return "", unauthorised(err)
	}
	name, sub, _ := strings.Cut(rel, "/")
	root, ok := c.rootNamed(name)
	if !ok {
		return "", unauthorised(fmt.Errorf("%w: %q", ErrNotShared, name))
	}

	full := root.dir
	if sub != "" {
		full = filepath.Join(root.dir, filepath.FromSlash(sub))
	}
	if !within(root.dir, full) {
		return "", unauthorised(fmt.Errorf("%w: %q leaves the share", ErrNotShared, wirePath))
	}
	// EvalSymlinks after the join, because a link inside the root can point
	// anywhere. A path that does not exist yet cannot be a link, so a
	// resolution failure there is left to the caller's os.Open to report.
	if resolved, err := filepath.EvalSymlinks(full); err == nil {
		if !within(root.dir, resolved) {
			return "", unauthorised(fmt.Errorf("%w: %q is a link out of the share", ErrNotShared, wirePath))
		}
		full = resolved
	}
	return full, nil
}

func (c *Capability) rootNamed(name string) (resolvedRoot, bool) {
	for _, r := range c.roots {
		if r.name == name {
			return r, true
		}
	}
	return resolvedRoot{}, false
}

func within(root, full string) bool {
	return full == root || strings.HasPrefix(full, root+string(filepath.Separator))
}

// writeMessage puts one response envelope on the stream.
func writeMessage(st session.Stream, msgType uint16, msg proto.Message) error {
	payload, err := proto.Marshal(msg)
	if err != nil {
		return err
	}
	return session.EncodeEnvelope(st, session.Envelope{
		Version: session.EnvelopeVersion,
		CapID:   CapID,
		MsgType: msgType,
		Payload: payload,
	})
}

// fsErr maps a filesystem failure onto a §10 code the peer can act on.
func fsErr(wirePath string, err error) error {
	switch {
	case errors.Is(err, os.ErrNotExist):
		return protoErr(session.CodeNotFound, err, "%s does not exist", wirePath)
	case errors.Is(err, os.ErrPermission):
		// The source cannot read it either. UNAUTHORISED would tell the peer to
		// authenticate, which would not help.
		return protoErr(session.CodeRejected, err, "%s cannot be read on the source device", wirePath)
	default:
		return fmt.Errorf("remotefs: %s: %w", wirePath, err)
	}
}

func protoErr(code session.ErrorCode, cause error, format string, args ...any) error {
	return &session.ProtocolError{Code: code, Msg: fmt.Sprintf(format, args...), Err: cause}
}

// unauthorised turns a share refusal into §11.1's required code.
func unauthorised(err error) error {
	if errors.Is(err, ErrNotShared) || errors.Is(err, ErrUnsafePath) {
		return &session.ProtocolError{Code: session.CodeUnauthorised, Msg: err.Error(), Err: err}
	}
	return err
}

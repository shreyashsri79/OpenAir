package daemon

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/shreyashsri79/openair/internal/caps/remotefs"
	"github.com/shreyashsri79/openair/internal/ipc"
	openairv1 "github.com/shreyashsri79/openair/internal/wire/openair/v1"
)

// Streaming, M11 (§11.2, §11.4). The daemon turns a remote file into a local
// HTTP URL, so a media player opens `http://127.0.0.1:port/...` and every Range
// request it makes becomes a range read on the wire.
//
// HTTP is here for one reason: every player already speaks it, and its Range
// header is the same primitive §11.2 defines. `http.ServeContent` maps one onto
// the other given an io.ReadSeeker, which is exactly what remotefs.Reader is —
// so seeking in a remote file is a player doing what it always does, with no
// plugin and nothing mounted.
//
// # What keeps this from being a hole in the machine
//
// A loopback HTTP server handing out another device's files is worth being
// careful about, so it is all three of these at once:
//
//   - Bound to 127.0.0.1 only, so nothing off this machine can reach it.
//   - Every URL carries 160 bits from crypto/rand. Loopback is not a trust
//     boundary — any local process can connect — so the token is what stops
//     another user's process on a shared machine from reading the stream, and
//     it is the URL rather than the path that is secret.
//   - Nothing is served until someone asks for it, each URL names exactly one
//     file, and an idle one is dropped. The server does not exist on a daemon
//     nobody has asked to stream anything.
//
// It is also why the URL is only ever printed to the shell that asked.

const (
	// streamIdle is how long a URL survives with nobody reading it. A player
	// paused for ten minutes is a plausible thing; a URL left behind for the
	// rest of the daemon's life is not.
	streamIdle = 15 * time.Minute

	// streamCacheBytes caps the on-disk block cache for streaming (§11.4). It
	// holds another device's files, so it is encrypted and bounded; see
	// remotefs.NewCache.
	streamCacheBytes = 512 << 20

	// streamOpenTimeout bounds the stat that opening a stream does. Serving the
	// bytes afterwards is not bounded by it -- a film is long.
	streamOpenTimeout = 30 * time.Second
)

// streamServer is the loopback HTTP server, created on first use.
type streamServer struct {
	d *Daemon

	mu      sync.Mutex
	ln      net.Listener
	srv     *http.Server
	streams map[string]*streamEntry // token to entry
	byPath  map[string]string       // device\x00path to token
	cache   *remotefs.Cache
	cacheAt string
}

// streamEntry is one file being served.
type streamEntry struct {
	device string
	path   string
	mime   string
	size   uint64

	// open makes a fresh Reader. Each HTTP request gets its own, because a
	// player may have two in flight — one playing and one probing after a seek
	// — and they must not share a position.
	open func(ctx context.Context) (*remotefs.Reader, error)

	mu   sync.Mutex
	used time.Time
}

// onStream answers a shell asking for a URL.
func (d *Daemon) onStream(ctx context.Context, c *client, payload []byte) {
	var req openairv1.DaemonStreamRequest
	if !unmarshal(c, payload, &req) {
		return
	}
	reply := &openairv1.DaemonStreamResponse{RequestId: req.GetRequestId()}

	if req.GetStop() {
		if err := d.stopStream(req.GetDevice(), req.GetPath()); err != nil {
			reply.Error = err.Error()
		}
		_ = c.peer.Send(ipc.MsgStreamResponse, reply)
		return
	}

	url, mime, size, err := d.stream(ctx, req.GetDevice(), req.GetPath())
	if err != nil {
		reply.Error = err.Error()
	} else {
		reply.Url, reply.Mime, reply.Size = url, mime, size
	}
	_ = c.peer.Send(ipc.MsgStreamResponse, reply)
}

// stream publishes one remote file on the loopback server and returns its URL.
func (d *Daemon) stream(ctx context.Context, device, remotePath string) (url, mime string, size uint64, err error) {
	if remotePath == "" {
		return "", "", 0, errors.New("no path to stream")
	}

	statCtx, cancel := context.WithTimeout(ctx, streamOpenTimeout)
	defer cancel()

	sess, err := d.sessionTo(statCtx, device)
	if err != nil {
		return "", "", 0, err
	}
	entry, err := d.rfs.Stat(statCtx, sess, remotePath)
	if err != nil {
		return "", "", 0, err
	}
	if entry.IsDir {
		return "", "", 0, fmt.Errorf("%s is a directory", remotePath)
	}

	srv, err := d.streamer()
	if err != nil {
		return "", "", 0, err
	}

	// The session is resolved again per reader rather than captured: a stream
	// held open across a network change (M9) should follow the device, not die
	// with the session it was opened on.
	peer := entry
	open := func(ctx context.Context) (*remotefs.Reader, error) {
		sess, err := d.sessionTo(ctx, device)
		if err != nil {
			return nil, err
		}
		return d.rfs.Open(ctx, sess, remotePath, remotefs.OpenOptions{
			Size:       peer.Size,
			ModifiedAt: peer.ModifiedAt,
			Cache:      srv.blockCache(),
		})
	}

	token, err := srv.publish(device, remotePath, entry.MIME, entry.Size, open)
	if err != nil {
		return "", "", 0, err
	}
	d.cfg.Logf("streaming %s from %s at %s", remotePath, device, srv.url(token))
	return srv.url(token), entry.MIME, entry.Size, nil
}

// stopStream withdraws a URL.
func (d *Daemon) stopStream(device, remotePath string) error {
	d.mu.Lock()
	srv := d.streams
	d.mu.Unlock()
	if srv == nil {
		return errors.New("nothing is being streamed")
	}
	if !srv.withdraw(device, remotePath) {
		return fmt.Errorf("%s is not being streamed", remotePath)
	}
	return nil
}

// streamer returns the loopback server, starting it the first time.
func (d *Daemon) streamer() (*streamServer, error) {
	d.mu.Lock()
	if d.streams != nil {
		srv := d.streams
		d.mu.Unlock()
		return srv, nil
	}
	d.mu.Unlock()

	srv := &streamServer{
		d:       d,
		streams: make(map[string]*streamEntry),
		byPath:  make(map[string]string),
	}
	// Loopback only. This is not a file server for the network; it is a bridge
	// between one player and one daemon on one machine.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	srv.ln = ln
	srv.srv = &http.Server{
		Handler:           srv,
		ReadHeaderTimeout: 10 * time.Second,
	}

	d.mu.Lock()
	if d.streams != nil {
		// Another shell raced us here; theirs is already listening.
		existing := d.streams
		d.mu.Unlock()
		ln.Close()
		return existing, nil
	}
	d.streams = srv
	d.mu.Unlock()

	go srv.srv.Serve(ln)
	go srv.reap()
	return srv, nil
}

// blockCache is the shared, encrypted, size-capped cache every stream reads
// through (§11.4). It is created on first use and removed when the daemon
// stops.
func (s *streamServer) blockCache() *remotefs.Cache {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cache != nil {
		return s.cache
	}
	dir, err := os.MkdirTemp("", "openair-stream-")
	if err != nil {
		s.d.cfg.Logf("streaming without a cache: %v", err)
		return nil
	}
	cache, err := remotefs.NewCache(filepath.Join(dir, "blocks"), streamCacheBytes)
	if err != nil {
		s.d.cfg.Logf("streaming without a cache: %v", err)
		os.RemoveAll(dir)
		return nil
	}
	s.cache, s.cacheAt = cache, dir
	return s.cache
}

// publish registers a file and returns its token, reusing the one already
// issued for the same file so that asking twice does not leak URLs.
func (s *streamServer) publish(device, remotePath, mime string, size uint64, open func(context.Context) (*remotefs.Reader, error)) (string, error) {
	key := device + "\x00" + remotePath

	s.mu.Lock()
	defer s.mu.Unlock()
	if token, ok := s.byPath[key]; ok {
		e := s.streams[token]
		e.mu.Lock()
		e.used = time.Now()
		e.mu.Unlock()
		return token, nil
	}

	token, err := streamToken()
	if err != nil {
		return "", err
	}
	s.streams[token] = &streamEntry{
		device: device,
		path:   remotePath,
		mime:   mime,
		size:   size,
		open:   open,
		used:   time.Now(),
	}
	s.byPath[key] = token
	return token, nil
}

// withdraw stops serving a file.
func (s *streamServer) withdraw(device, remotePath string) bool {
	key := device + "\x00" + remotePath

	s.mu.Lock()
	defer s.mu.Unlock()
	token, ok := s.byPath[key]
	if !ok {
		return false
	}
	delete(s.byPath, key)
	delete(s.streams, token)
	return true
}

// url is the loopback address of one token. The file's base name is on the end
// because players read the extension: without it, some of them refuse to guess
// a demuxer.
func (s *streamServer) url(token string) string {
	s.mu.Lock()
	e := s.streams[token]
	addr := s.ln.Addr().String()
	s.mu.Unlock()

	name := "stream"
	if e != nil {
		if base := path.Base(e.path); base != "." && base != "/" {
			name = base
		}
	}
	return "http://" + addr + "/s/" + token + "/" + name
}

// ServeHTTP serves one Range request out of one remote file.
func (s *streamServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	rest, ok := strings.CutPrefix(r.URL.Path, "/s/")
	if !ok {
		http.NotFound(w, r)
		return
	}
	token, _, _ := strings.Cut(rest, "/")

	s.mu.Lock()
	e := s.streams[token]
	s.mu.Unlock()
	if e == nil {
		// No hint about whether the token was wrong or expired: a caller
		// guessing tokens learns nothing either way.
		http.NotFound(w, r)
		return
	}
	e.mu.Lock()
	e.used = time.Now()
	e.mu.Unlock()

	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "only GET and HEAD", http.StatusMethodNotAllowed)
		return
	}

	reader, err := e.open(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer reader.Close()

	if e.mime != "" {
		w.Header().Set("Content-Type", e.mime)
	}
	// ServeContent is what turns a Range header into a seek, and a seek into
	// §11.2 range reads. The zero modtime keeps it from serving conditionally
	// on something the source did not promise.
	http.ServeContent(w, r, path.Base(e.path), time.Time{}, reader)
}

// reap drops streams nobody is reading, and stops when the daemon does.
func (s *streamServer) reap() {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()

	for range ticker.C {
		s.mu.Lock()
		if s.streams == nil {
			s.mu.Unlock()
			return
		}
		now := time.Now()
		for token, e := range s.streams {
			e.mu.Lock()
			idle := now.Sub(e.used)
			e.mu.Unlock()
			if idle > streamIdle {
				delete(s.streams, token)
				delete(s.byPath, e.device+"\x00"+e.path)
			}
		}
		s.mu.Unlock()
	}
}

// close stops the server and removes the cache.
func (s *streamServer) close() {
	s.mu.Lock()
	srv, ln := s.srv, s.ln
	cache, dir := s.cache, s.cacheAt
	s.streams = nil
	s.byPath = nil
	s.cache = nil
	s.mu.Unlock()

	if srv != nil {
		srv.Close()
	}
	if ln != nil {
		ln.Close()
	}
	if cache != nil {
		cache.Close()
	}
	if dir != "" {
		os.RemoveAll(dir)
	}
}

// streamToken is 160 bits of randomness in the URL. It is the whole access
// control on loopback, so it comes from crypto/rand and is long enough that
// guessing is not a strategy.
func streamToken() (string, error) {
	b := make([]byte, 20)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b)), nil
}

// Compile-time assurance that a Reader is what ServeContent wants; if this ever
// stops holding, streaming silently becomes a whole-file download.
var _ interface {
	io.ReadSeeker
} = (*remotefs.Reader)(nil)

package daemon

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/shreyashsri79/openair/internal/caps/remotefs"
	"github.com/shreyashsri79/openair/internal/ipc"
	openairv1 "github.com/shreyashsri79/openair/internal/wire/openair/v1"
)

// Browsing, M10 (§11). The daemon is the side that holds the session and the
// unlock, so a shell asks it for a path and gets entries back.
//
// Two things stay on this side of the socket deliberately. The Owned proof is
// signed by the privilege key, which never leaves the daemon (D-20). And the
// range reads are a loop — a 40 GB file is not something to hand across a local
// socket one envelope at a time when the caller only wanted it on disk.

// browseTimeout bounds one listing. A source answering a directory listing is
// doing a readdir, not a transfer.
const browseTimeout = 30 * time.Second

// onBrowse answers a shell's list request.
func (d *Daemon) onBrowse(ctx context.Context, c *client, payload []byte) {
	var req openairv1.DaemonBrowseRequest
	if !unmarshal(c, payload, &req) {
		return
	}
	reply := &openairv1.DaemonBrowseResponse{RequestId: req.GetRequestId()}

	entries, truncated, err := d.browse(ctx, req.GetDevice(), req.GetPath(),
		int(req.GetOffset()), int(req.GetLimit()))
	if err != nil {
		reply.Error = err.Error()
	} else {
		reply.Truncated = truncated
		for _, e := range entries {
			reply.Entries = append(reply.Entries, &openairv1.FileStat{
				Path:       e.Path,
				Size:       e.Size,
				ModifiedAt: e.ModifiedAt,
				IsDir:      e.IsDir,
				Mode:       e.Mode,
				Mime:       e.MIME,
			})
		}
	}
	_ = c.peer.Send(ipc.MsgBrowseResponse, reply)
}

func (d *Daemon) browse(ctx context.Context, target, path string, offset, limit int) ([]remotefs.Entry, bool, error) {
	ctx, cancel := context.WithTimeout(ctx, browseTimeout)
	defer cancel()

	sess, err := d.sessionTo(ctx, target)
	if err != nil {
		return nil, false, err
	}
	return d.rfs.List(ctx, sess, path, offset, limit)
}

// onFetch answers a shell's fetch request: copy a remote file, or part of one,
// to a local path.
func (d *Daemon) onFetch(ctx context.Context, c *client, payload []byte) {
	var req openairv1.DaemonFetchRequest
	if !unmarshal(c, payload, &req) {
		return
	}
	reply := &openairv1.DaemonFetchResponse{RequestId: req.GetRequestId(), Dest: req.GetDest()}

	written, err := d.fetch(ctx, &req)
	reply.BytesWritten = written
	if err != nil {
		reply.Error = err.Error()
	}
	_ = c.peer.Send(ipc.MsgFetchResponse, reply)
}

// fetch reads a range over remotefs and writes it to a local file.
//
// It is a loop of range reads rather than a transfer: §11.2's read is the
// primitive, and a source serving it does not know whether the client is
// copying the file or seeking around inside it (§11's dumb server).
func (d *Daemon) fetch(ctx context.Context, req *openairv1.DaemonFetchRequest) (uint64, error) {
	sess, err := d.sessionTo(ctx, req.GetDevice())
	if err != nil {
		return 0, err
	}

	dest := req.GetDest()
	if dest == "" {
		return 0, errors.New("no local path to write to")
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
		return 0, err
	}
	// A part file, renamed at the end, for the reason files uses one: a fetch
	// interrupted halfway must not leave something that looks like the file.
	part := dest + ".oapart"
	f, err := os.OpenFile(part, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return 0, err
	}
	defer func() {
		f.Close()
		os.Remove(part)
	}()

	var (
		written uint64
		offset  = req.GetOffset()
		remain  = req.GetLength()
		buf     = make([]byte, remotefs.MaxReadLength)
	)
	for {
		want := buf
		if remain > 0 && remain < uint64(len(want)) {
			want = want[:remain]
		}
		n, eof, err := d.rfs.ReadAt(ctx, sess, req.GetPath(), offset, want)
		if err != nil {
			return written, err
		}
		if n > 0 {
			if _, err := f.Write(want[:n]); err != nil {
				return written, err
			}
			written += uint64(n)
			offset += uint64(n)
			if remain > 0 {
				remain -= uint64(n)
			}
		}
		if eof || (req.GetLength() > 0 && remain == 0) {
			break
		}
		if n == 0 {
			return written, io.ErrNoProgress
		}
	}

	if err := f.Close(); err != nil {
		return written, err
	}
	if err := os.Rename(part, dest); err != nil {
		return written, err
	}
	d.cfg.Logf("fetched %s from %s into %s (%d bytes)",
		req.GetPath(), req.GetDevice(), dest, written)
	return written, nil
}

// shareSummary is what `openair status` says about what this device offers.
func (d *Daemon) shareSummary() []string {
	if d.rfs == nil {
		return nil
	}
	return d.rfs.Roots()
}

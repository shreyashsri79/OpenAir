package remotefs

import (
	"context"
	"errors"
	"fmt"
	"io"

	"google.golang.org/protobuf/proto"

	"github.com/shreyashsri79/openair/internal/session"
	openairv1 "github.com/shreyashsri79/openair/internal/wire/openair/v1"
)

// The client half of §11. One stream per request, opened with an AuthProof on
// it (D-71), a single request envelope, and a single response.
//
// There is deliberately no cache and no read-ahead here. §11.4 puts both on the
// client, and M11 is where they belong — a browse that pre-fetched would be
// making decisions on behalf of a caller that has not said what it is doing
// with the bytes. What this file owes M11 is a `ReadAt` that is cheap to call
// repeatedly and honest about short reads.

// Entry is one FileStat, in Go terms.
type Entry struct {
	Path       string
	Size       uint64
	ModifiedAt int64
	IsDir      bool
	Mode       uint32
	MIME       string
}

func entryOf(s *openairv1.FileStat) Entry {
	return Entry{
		Path:       s.GetPath(),
		Size:       s.GetSize(),
		ModifiedAt: s.GetModifiedAt(),
		IsDir:      s.GetIsDir(),
		Mode:       s.GetMode(),
		MIME:       s.GetMime(),
	}
}

// ErrLockedPeer reports a refusal this device can fix: the far end requires
// Owned and this one has not unlocked for it. It is separated from a plain
// refusal because the two send a user to different places — one says
// "authenticate", the other says "you are not allowed at all" (§10).
var ErrLockedPeer = errors.New("remotefs: the peer refused an unproven request; unlock for that device first")

// ErrRefused is a refusal that authenticating will not fix: a path outside
// every share, or a device that no longer records this one as Owned. §11.1
// gives both the same code as a missing proof, deliberately -- a peer learns
// nothing about which paths exist -- so the two are told apart by whether this
// device had sent a proof at all.
var ErrRefused = errors.New("remotefs: that device refused the request; the path may not be shared, or this device may no longer be Owned there")

// Stat asks about one path. An empty path describes the share list.
func (c *Capability) Stat(ctx context.Context, sess session.Session, path string) (Entry, error) {
	var resp openairv1.StatResponse
	if err := c.request(ctx, sess, MsgStatRequest, &openairv1.StatRequest{Path: path},
		MsgStatResponse, &resp, nil); err != nil {
		return Entry{}, err
	}
	return entryOf(resp.GetStat()), nil
}

// List reads one page of a directory. An empty path lists the shares.
//
// truncated says more remain: ask again at offset+len(entries). §11.1 makes
// this paginated so that a directory of 100k entries is many small responses
// rather than one refused envelope.
func (c *Capability) List(ctx context.Context, sess session.Session, path string, offset, limit int) (entries []Entry, truncated bool, err error) {
	var resp openairv1.ListResponse
	req := &openairv1.ListRequest{Path: path, Offset: uint32(offset), Limit: uint32(limit)}
	if err := c.request(ctx, sess, MsgListRequest, req, MsgListResponse, &resp, nil); err != nil {
		return nil, false, err
	}
	entries = make([]Entry, 0, len(resp.GetEntries()))
	for _, e := range resp.GetEntries() {
		entries = append(entries, entryOf(e))
	}
	return entries, resp.GetTruncated(), nil
}

// ReadAt fetches a byte range into p (§11.2).
//
// It returns what arrived, which may be less than len(p) — at end of file, or
// because the source chose a smaller quantum. §11.2 requires clients to handle
// that, so this reports n and eof rather than pretending to fill the buffer.
func (c *Capability) ReadAt(ctx context.Context, sess session.Session, path string, offset uint64, p []byte) (n int, eof bool, err error) {
	if len(p) == 0 {
		return 0, false, nil
	}
	var resp openairv1.ReadResponse
	req := &openairv1.ReadRequest{Path: path, Offset: offset, Length: uint64(len(p))}

	err = c.request(ctx, sess, MsgReadRequest, req, MsgReadResponse, &resp, func(st session.Stream) error {
		length := resp.GetLength()
		if length > uint64(len(p)) {
			return fmt.Errorf("remotefs: the source offered %d bytes for a %d-byte read", length, len(p))
		}
		if resp.GetOffset() != offset {
			return fmt.Errorf("remotefs: asked for offset %d, the source answered for %d", offset, resp.GetOffset())
		}
		read, rerr := io.ReadFull(st, p[:length])
		n = read
		if rerr != nil && !errors.Is(rerr, io.EOF) && !errors.Is(rerr, io.ErrUnexpectedEOF) {
			return rerr
		}
		if uint64(read) != length {
			return fmt.Errorf("remotefs: the source announced %d bytes and sent %d", length, read)
		}
		return nil
	})
	if err != nil {
		return 0, false, err
	}
	return n, resp.GetEof(), nil
}

// ReadFull fetches a whole range, issuing as many reads as the source's quantum
// requires. It is the convenience `openair get` wants and the thing a media
// client should not use.
func (c *Capability) ReadFull(ctx context.Context, sess session.Session, path string, offset uint64, p []byte) (int, error) {
	total := 0
	for total < len(p) {
		n, eof, err := c.ReadAt(ctx, sess, path, offset+uint64(total), p[total:])
		if err != nil {
			return total, err
		}
		total += n
		if eof {
			if total == len(p) {
				return total, nil
			}
			return total, io.EOF
		}
		if n == 0 {
			return total, io.ErrNoProgress
		}
	}
	return total, nil
}

// Thumb asks the source to render a preview (§11.3). maxDimension bounds the
// longer side.
func (c *Capability) Thumb(ctx context.Context, sess session.Session, path string, maxDimension int) (mime string, image []byte, err error) {
	var resp openairv1.ThumbResponse
	req := &openairv1.ThumbRequest{Path: path, MaxDimension: uint32(maxDimension)}
	if err := c.request(ctx, sess, MsgThumbRequest, req, MsgThumbResponse, &resp, nil); err != nil {
		return "", nil, err
	}
	return resp.GetMime(), resp.GetImage(), nil
}

// request runs one stream: proof, request, response, and whatever raw bytes the
// caller then wants to read.
func (c *Capability) request(ctx context.Context, sess session.Session, reqType uint16, req proto.Message, wantType uint16, resp proto.Message, after func(session.Stream) error) error {
	st, proven, err := session.OpenOwnedStreamIfUnlocked(ctx, sess, CapID, reqType)
	if err != nil {
		return err
	}
	defer st.Close()

	payload, err := proto.Marshal(req)
	if err != nil {
		return err
	}
	if err := session.EncodeEnvelope(st, session.Envelope{
		Version: session.EnvelopeVersion,
		CapID:   CapID,
		MsgType: reqType,
		Payload: payload,
	}); err != nil {
		return translate(err, proven)
	}

	env, err := session.DecodeEnvelope(st)
	if err != nil {
		return translate(err, proven)
	}
	if env.CapID != CapID || env.MsgType != wantType {
		return fmt.Errorf("remotefs: expected capID %d msgType %d, got capID %d msgType %d",
			CapID, wantType, env.CapID, env.MsgType)
	}
	if err := proto.Unmarshal(env.Payload, resp); err != nil {
		return err
	}
	if after != nil {
		return after(st)
	}
	return nil
}

// translate turns a stream reset into the §10 code the source chose, so the
// caller can tell "not allowed" from "not found" from "the source is busy".
//
// proven says whether this request carried an AuthProof, which is what makes
// UNAUTHORISED readable. §11.1 uses that one code for two different situations
// -- "you have not authenticated" and "that path is not yours to ask about" --
// and only the sender knows which of them it could still be: a request that
// went out unproven can be fixed by unlocking, and one that went out proven
// cannot.
func translate(err error, proven bool) error {
	code, ok := session.StreamErrorCode(err)
	if !ok {
		return err
	}
	switch code {
	case session.CodeUnauthorised:
		if proven {
			return fmt.Errorf("%w: %w", ErrRefused, err)
		}
		return fmt.Errorf("%w: %w", ErrLockedPeer, err)
	case session.CodeNotFound:
		return fmt.Errorf("remotefs: no such path on that device: %w", err)
	case session.CodeCapabilityUnavailable:
		return fmt.Errorf("remotefs: that device is not sharing anything: %w", err)
	}
	return &session.ProtocolError{Code: code, Msg: "the source refused the request", Err: err}
}

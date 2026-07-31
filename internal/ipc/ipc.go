// Package ipc is the local link between `openaird` and the shells that drive
// it -- the CLI today, a tray UI later.
//
// It carries the session envelope (PROTOCOL.md §3) over a unix socket or a
// Windows named pipe rather than gRPC (D-29): one wire format to specify, test
// and generate goldens for instead of two, and a clipboard push from a tray UI
// is byte-for-byte the message the network carries. What gRPC would have
// supplied and this does not is request/response correlation, which is the
// request_id field on every message in daemon.proto plus the pending-call map
// in Peer.
//
// The socket is a local trust boundary, not hygiene: anything that can open it
// drives the daemon, with this device's identity key behind it. Access control
// is filesystem permissions plus, on Linux, a peer-credential check, and on
// Windows a pipe ACL naming the owning user alone. See transport_*.go.
package ipc

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"sync/atomic"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/shreyashsri79/openair/internal/session"
	openairv1 "github.com/shreyashsri79/openair/internal/wire/openair/v1"
)

// CapID is the daemon control plane's capability ID (PROTOCOL.md Appendix B,
// as extended by D-51). It never appears on a network session: a peer sending
// capID 7 over QUIC reaches no registered handler and is ignored under §3.1.
const CapID byte = 0x07

// Message types, converted from the generated enum. DaemonMessageType is like
// every other *MessageType enum in these schemas: the values ARE the wire
// values, with 0 invalid (D-39). No offset applies.
const (
	MsgStatusRequest      = uint16(openairv1.DaemonMessageType_DAEMON_MESSAGE_TYPE_STATUS_REQUEST)
	MsgStatusResponse     = uint16(openairv1.DaemonMessageType_DAEMON_MESSAGE_TYPE_STATUS_RESPONSE)
	MsgDeviceListRequest  = uint16(openairv1.DaemonMessageType_DAEMON_MESSAGE_TYPE_DEVICE_LIST_REQUEST)
	MsgDeviceListResponse = uint16(openairv1.DaemonMessageType_DAEMON_MESSAGE_TYPE_DEVICE_LIST_RESPONSE)
	MsgSendRequest        = uint16(openairv1.DaemonMessageType_DAEMON_MESSAGE_TYPE_SEND_REQUEST)
	MsgSendResponse       = uint16(openairv1.DaemonMessageType_DAEMON_MESSAGE_TYPE_SEND_RESPONSE)
	MsgPairRequest        = uint16(openairv1.DaemonMessageType_DAEMON_MESSAGE_TYPE_PAIR_REQUEST)
	MsgPairResponse       = uint16(openairv1.DaemonMessageType_DAEMON_MESSAGE_TYPE_PAIR_RESPONSE)
	MsgSubscribeRequest   = uint16(openairv1.DaemonMessageType_DAEMON_MESSAGE_TYPE_SUBSCRIBE_REQUEST)
	MsgSubscribeResponse  = uint16(openairv1.DaemonMessageType_DAEMON_MESSAGE_TYPE_SUBSCRIBE_RESPONSE)
	MsgEvent              = uint16(openairv1.DaemonMessageType_DAEMON_MESSAGE_TYPE_EVENT)
	MsgPrompt             = uint16(openairv1.DaemonMessageType_DAEMON_MESSAGE_TYPE_PROMPT)
	MsgPromptResponse     = uint16(openairv1.DaemonMessageType_DAEMON_MESSAGE_TYPE_PROMPT_RESPONSE)
	MsgClipboardRequest   = uint16(openairv1.DaemonMessageType_DAEMON_MESSAGE_TYPE_CLIPBOARD_REQUEST)
	MsgClipboardResponse  = uint16(openairv1.DaemonMessageType_DAEMON_MESSAGE_TYPE_CLIPBOARD_RESPONSE)
	MsgError              = uint16(openairv1.DaemonMessageType_DAEMON_MESSAGE_TYPE_ERROR)
	MsgUnlockRequest      = uint16(openairv1.DaemonMessageType_DAEMON_MESSAGE_TYPE_UNLOCK_REQUEST)
	MsgUnlockResponse     = uint16(openairv1.DaemonMessageType_DAEMON_MESSAGE_TYPE_UNLOCK_RESPONSE)
	MsgLockRequest        = uint16(openairv1.DaemonMessageType_DAEMON_MESSAGE_TYPE_LOCK_REQUEST)
	MsgLockResponse       = uint16(openairv1.DaemonMessageType_DAEMON_MESSAGE_TYPE_LOCK_RESPONSE)
	MsgTrustRequest       = uint16(openairv1.DaemonMessageType_DAEMON_MESSAGE_TYPE_TRUST_REQUEST)
	MsgTrustResponse      = uint16(openairv1.DaemonMessageType_DAEMON_MESSAGE_TYPE_TRUST_RESPONSE)
	MsgBrowseRequest      = uint16(openairv1.DaemonMessageType_DAEMON_MESSAGE_TYPE_BROWSE_REQUEST)
	MsgBrowseResponse     = uint16(openairv1.DaemonMessageType_DAEMON_MESSAGE_TYPE_BROWSE_RESPONSE)
	MsgFetchRequest       = uint16(openairv1.DaemonMessageType_DAEMON_MESSAGE_TYPE_FETCH_REQUEST)
	MsgFetchResponse      = uint16(openairv1.DaemonMessageType_DAEMON_MESSAGE_TYPE_FETCH_RESPONSE)
	MsgNotifyRequest      = uint16(openairv1.DaemonMessageType_DAEMON_MESSAGE_TYPE_NOTIFY_REQUEST)
	MsgNotifyResponse     = uint16(openairv1.DaemonMessageType_DAEMON_MESSAGE_TYPE_NOTIFY_RESPONSE)
	MsgDismissRequest     = uint16(openairv1.DaemonMessageType_DAEMON_MESSAGE_TYPE_DISMISS_REQUEST)
	MsgDismissResponse    = uint16(openairv1.DaemonMessageType_DAEMON_MESSAGE_TYPE_DISMISS_RESPONSE)
	MsgStreamRequest      = uint16(openairv1.DaemonMessageType_DAEMON_MESSAGE_TYPE_STREAM_REQUEST)
	MsgStreamResponse     = uint16(openairv1.DaemonMessageType_DAEMON_MESSAGE_TYPE_STREAM_RESPONSE)
)

// isReply reports whether a message type answers an earlier request of ours.
// Everything else is delivered to the connection's handler.
// isReply says which message types answer a request rather than start one.
//
// Every response type must be listed. A missing one is not a compile error and
// not a runtime error either: the reply is routed as though it were an inbound
// request, no handler claims it, and the caller waits until its context expires.
// TestEveryResponseTypeIsAReply is what stops that from being discovered by a
// twenty-second timeout.
func isReply(t uint16) bool {
	switch t {
	case MsgStatusResponse, MsgDeviceListResponse, MsgSendResponse,
		MsgPairResponse, MsgSubscribeResponse, MsgClipboardResponse,
		MsgUnlockResponse, MsgLockResponse, MsgTrustResponse,
		MsgBrowseResponse, MsgFetchResponse,
		MsgNotifyResponse, MsgDismissResponse, MsgStreamResponse, MsgError:
		return true
	}
	return false
}

// correlated reports whether a message type is an answer to something, and so
// is routed by request ID rather than to a handler.
//
// Two tables, one rule: replies answer our requests, and a prompt response
// answers a prompt we sent. The direction is unambiguous from the type, which
// is why one connection can carry both without a wrapper (D-51).
func correlated(t uint16) bool { return isReply(t) || t == MsgPromptResponse }

// Error is a DaemonError received in place of the reply that was expected.
type Error struct {
	Code    uint32
	Message string
}

func (e *Error) Error() string {
	if e.Code != 0 {
		return fmt.Sprintf("daemon: %s (%s)", e.Message, session.ErrorCode(e.Code))
	}
	return "daemon: " + e.Message
}

// ErrClosed is returned by every pending and subsequent call once the
// connection has gone.
var ErrClosed = errors.New("ipc: connection closed")

// Peer is one end of an IPC connection. Both ends are symmetric: the client
// issues requests and answers prompts, the daemon answers requests and issues
// prompts, and the same code carries both.
type Peer struct {
	conn net.Conn

	wmu sync.Mutex // one writer at a time; EncodeEnvelope writes each frame whole
	ids atomic.Uint64

	mu      sync.Mutex
	pending map[uint64]chan reply // replies to our requests
	prompts map[uint64]chan reply // replies to our prompts
	closed  bool

	handler func(ctx context.Context, p *Peer, msgType uint16, payload []byte)

	done chan struct{}
	err  error
}

type reply struct {
	typ     uint16
	payload []byte
}

// NewPeer wraps an established connection. handler is called for every inbound
// message that is not a reply -- requests on the daemon side, events and
// prompts on the client side. It runs on its own goroutine per message, so a
// handler that blocks does not stall the read loop; ordering between messages
// is therefore not guaranteed and no daemon message depends on it.
func NewPeer(conn net.Conn, handler func(ctx context.Context, p *Peer, msgType uint16, payload []byte)) *Peer {
	return &Peer{
		conn:    conn,
		pending: make(map[uint64]chan reply),
		prompts: make(map[uint64]chan reply),
		handler: handler,
		done:    make(chan struct{}),
	}
}

// Serve runs the read loop until the connection ends or ctx is cancelled.
func (p *Peer) Serve(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// A cancelled context must interrupt a blocked Read; closing the connection
	// is the only thing that does that for a net.Conn.
	go func() {
		<-ctx.Done()
		_ = p.conn.Close()
	}()

	var wg sync.WaitGroup
	defer wg.Wait()

	// Buffered, so DecodeEnvelope's header read and payload read are not two
	// syscalls per frame.
	r := bufio.NewReader(p.conn)
	for {
		env, err := session.DecodeEnvelope(r)
		if err != nil {
			p.fail(err)
			if errors.Is(err, io.EOF) || ctx.Err() != nil {
				return nil
			}
			return err
		}
		// §3.1: an unrecognised capID or msgType is ignored, never fatal. The
		// same rule that makes mixed-version fleets viable on the network makes
		// a newer CLI safe against an older daemon here.
		if env.CapID != CapID {
			continue
		}
		p.dispatch(ctx, &wg, env.MsgType, env.Payload)
	}
}

func (p *Peer) dispatch(ctx context.Context, wg *sync.WaitGroup, msgType uint16, payload []byte) {
	id := peekRequestID(payload)

	var table map[uint64]chan reply
	switch {
	case isReply(msgType):
		table = p.pending
	case msgType == MsgPromptResponse:
		table = p.prompts
	}
	if table != nil {
		p.mu.Lock()
		ch, ok := table[id]
		if ok {
			delete(table, id)
		}
		p.mu.Unlock()
		if ok {
			ch <- reply{msgType, payload}
		}
		// A reply to nothing is a late answer to a call that already timed out.
		// Dropping it is correct; killing the connection over it is not.
		return
	}

	if p.handler == nil {
		return
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		p.handler(ctx, p, msgType, payload)
	}()
}

// Send writes one message with no reply expected. The request_id already set on
// msg is left alone.
func (p *Peer) Send(msgType uint16, msg proto.Message) error {
	payload, err := proto.Marshal(msg)
	if err != nil {
		return err
	}
	p.wmu.Lock()
	defer p.wmu.Unlock()
	select {
	case <-p.done:
		return ErrClosed
	default:
	}
	return session.EncodeEnvelope(p.conn, session.Envelope{
		Version: session.EnvelopeVersion,
		CapID:   CapID,
		MsgType: msgType,
		Payload: payload,
	})
}

// Do sends a request and waits for its reply, which it unmarshals into out.
//
// The request ID is allocated here and written into msg, so callers never set
// it: a caller-chosen ID is a caller-chosen collision.
func (p *Peer) Do(ctx context.Context, msgType uint16, msg proto.Message, out proto.Message) error {
	return p.call(ctx, msgType, msg, out, false)
}

// Ask sends a prompt and waits for the answer. It is Do in the other
// direction, with its own ID namespace so a prompt and a request cannot
// collide.
func (p *Peer) Ask(ctx context.Context, msg *openairv1.DaemonPrompt) (bool, error) {
	var resp openairv1.DaemonPromptResponse
	if err := p.call(ctx, MsgPrompt, msg, &resp, true); err != nil {
		return false, err
	}
	return resp.GetApprove(), nil
}

func (p *Peer) call(ctx context.Context, msgType uint16, msg, out proto.Message, prompt bool) error {
	id := p.ids.Add(1)
	setRequestID(msg, id)

	ch := make(chan reply, 1)
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return ErrClosed
	}
	if prompt {
		p.prompts[id] = ch
	} else {
		p.pending[id] = ch
	}
	p.mu.Unlock()

	defer func() {
		p.mu.Lock()
		if prompt {
			delete(p.prompts, id)
		} else {
			delete(p.pending, id)
		}
		p.mu.Unlock()
	}()

	if err := p.Send(msgType, msg); err != nil {
		return err
	}

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-p.done:
		if p.err != nil {
			return p.err
		}
		return ErrClosed
	case r := <-ch:
		if r.typ == MsgError {
			var e openairv1.DaemonError
			if err := proto.Unmarshal(r.payload, &e); err != nil {
				return err
			}
			return &Error{Code: e.GetCode(), Message: e.GetMessage()}
		}
		return proto.Unmarshal(r.payload, out)
	}
}

// Reply answers a request with the same ID it carried.
func (p *Peer) Reply(msgType uint16, requestID uint64, msg proto.Message) error {
	setRequestID(msg, requestID)
	return p.Send(msgType, msg)
}

// ReplyError answers a request with a failure. code is a PROTOCOL.md §10 value
// where one applies and zero where the failure is local to this device.
func (p *Peer) ReplyError(requestID uint64, code session.ErrorCode, format string, args ...any) error {
	return p.Reply(MsgError, requestID, &openairv1.DaemonError{
		Code:    uint32(code),
		Message: fmt.Sprintf(format, args...),
	})
}

// Close ends the connection and unblocks every pending call.
func (p *Peer) Close() error {
	p.fail(ErrClosed)
	return p.conn.Close()
}

// Done is closed when the connection ends, for callers that own a peer's
// lifetime rather than its read loop.
func (p *Peer) Done() <-chan struct{} { return p.done }

func (p *Peer) fail(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return
	}
	p.closed = true
	p.err = err
	close(p.done)
}

// peekRequestID reads field 1 of a daemon message without unmarshalling it.
//
// Every message in daemon.proto declares request_id as field 1, so a set
// request ID is always the first bytes on the wire: tag 0x08 then a varint.
// Reading it this way is what lets the read loop route a reply to its caller
// without knowing which message type it holds. TestEveryDaemonMessageHasRequestIDFirst
// is what keeps that invariant true as the schema grows.
//
// A zero request ID is not encoded at all in proto3, so an absent field 1 is
// correctly read as zero.
func peekRequestID(payload []byte) uint64 {
	if len(payload) < 2 || payload[0] != 0x08 {
		return 0
	}
	id, n := binary.Uvarint(payload[1:])
	if n <= 0 {
		return 0
	}
	return id
}

// setRequestID writes the ID into a message's request_id field reflectively,
// so the transport does not need an interface every message must implement.
func setRequestID(msg proto.Message, id uint64) {
	m := msg.ProtoReflect()
	fd := m.Descriptor().Fields().ByName("request_id")
	if fd == nil {
		return
	}
	m.Set(fd, protoreflect.ValueOfUint64(id))
}

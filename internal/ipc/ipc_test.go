package ipc

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	openairv1 "github.com/shreyashsri79/openair/internal/wire/openair/v1"
)

// TestEveryDaemonMessageHasRequestIDFirst protects the routing trick in
// peekRequestID: the read loop finds a reply's correlation ID without knowing
// which message type it is holding, and it can only do that while request_id
// is field 1 everywhere. A new message that numbers it differently would route
// every reply to request 0 and hang the caller instead of failing loudly.
func TestEveryDaemonMessageHasRequestIDFirst(t *testing.T) {
	fd := openairv1.File_openair_v1_daemon_proto
	msgs := fd.Messages()
	checked := 0
	for i := 0; i < msgs.Len(); i++ {
		m := msgs.Get(i)
		if !carriedInAnEnvelope(string(m.Name())) {
			continue
		}
		f := m.Fields().ByName("request_id")
		if f == nil {
			t.Errorf("%s has no request_id field", m.Name())
			continue
		}
		if f.Number() != 1 {
			t.Errorf("%s.request_id is field %d, must be 1", m.Name(), f.Number())
		}
		if f.Kind() != protoreflect.Uint64Kind {
			t.Errorf("%s.request_id is %s, must be uint64", m.Name(), f.Kind())
		}
		checked++
	}
	if checked < 10 {
		t.Fatalf("only %d daemon messages checked; the descriptor lookup is wrong", checked)
	}
}

// carriedInAnEnvelope reports whether a message is ever an envelope payload in
// its own right. Nested elements -- DaemonDevice inside a list response -- are
// not, and have no correlation ID to carry.
func carriedInAnEnvelope(name string) bool {
	for _, suffix := range []string{"Request", "Response", "Event", "Prompt", "Error"} {
		if len(name) >= len(suffix) && name[len(name)-len(suffix):] == suffix {
			return true
		}
	}
	return false
}

func TestPeekRequestIDMatchesTheEncodedMessage(t *testing.T) {
	for _, id := range []uint64{0, 1, 127, 128, 300, 1 << 20, ^uint64(0)} {
		b, err := proto.Marshal(&openairv1.DaemonStatusResponse{
			RequestId: id, DeviceId: "abcdefghijklmnop",
		})
		if err != nil {
			t.Fatal(err)
		}
		if got := peekRequestID(b); got != id {
			t.Errorf("peekRequestID = %d, want %d", got, id)
		}
	}
}

// pipePair wires two peers together over an in-memory connection.
func pipePair(t *testing.T, onA, onB func(context.Context, *Peer, uint16, []byte)) (*Peer, *Peer) {
	t.Helper()
	ac, bc := net.Pipe()
	a := NewPeer(ac, onA)
	b := NewPeer(bc, onB)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go a.Serve(ctx)
	go b.Serve(ctx)
	return a, b
}

// TestConcurrentRequestsGetTheirOwnReplies is the correlation gRPC would have
// supplied (D-29). Replies come back deliberately out of order.
func TestConcurrentRequestsGetTheirOwnReplies(t *testing.T) {
	const n = 50

	server := func(ctx context.Context, p *Peer, msgType uint16, payload []byte) {
		if msgType != MsgStatusRequest {
			return
		}
		var req openairv1.DaemonStatusRequest
		if err := proto.Unmarshal(payload, &req); err != nil {
			return
		}
		// Answer at a length-dependent delay so replies interleave.
		time.Sleep(time.Duration(req.GetRequestId()%7) * time.Millisecond)
		_ = p.Reply(MsgStatusResponse, req.GetRequestId(), &openairv1.DaemonStatusResponse{
			DeviceId: fmt.Sprintf("req-%d", req.GetRequestId()),
		})
	}

	client, _ := pipePair(t, nil, server)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var resp openairv1.DaemonStatusResponse
			if err := client.Do(ctx, MsgStatusRequest, &openairv1.DaemonStatusRequest{}, &resp); err != nil {
				errs <- err
				return
			}
			// The reply must name the request it answered.
			want := fmt.Sprintf("req-%d", resp.GetRequestId())
			if resp.GetDeviceId() != want {
				errs <- fmt.Errorf("reply %d carries %q, want %q",
					resp.GetRequestId(), resp.GetDeviceId(), want)
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

// TestPromptsAndRequestsDoNotCollide is why the two directions keep separate ID
// namespaces: both counters start at 1, and a shared map would deliver a
// prompt's answer to a request.
func TestPromptsAndRequestsDoNotCollide(t *testing.T) {
	var client *Peer

	server := func(ctx context.Context, p *Peer, msgType uint16, payload []byte) {
		if msgType != MsgStatusRequest {
			return
		}
		var req openairv1.DaemonStatusRequest
		_ = proto.Unmarshal(payload, &req)

		// Ask a question while the request is still outstanding: both are
		// ID 1 on their own side.
		ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		ok, err := p.Ask(ctx, &openairv1.DaemonPrompt{Text: "well?"})
		if err != nil {
			t.Errorf("Ask: %v", err)
		}
		if !ok {
			t.Error("prompt was answered no, want yes")
		}
		_ = p.Reply(MsgStatusResponse, req.GetRequestId(), &openairv1.DaemonStatusResponse{DeviceId: "ok"})
	}

	answerPrompts := func(ctx context.Context, p *Peer, msgType uint16, payload []byte) {
		if msgType != MsgPrompt {
			return
		}
		var pr openairv1.DaemonPrompt
		_ = proto.Unmarshal(payload, &pr)
		_ = p.Send(MsgPromptResponse, &openairv1.DaemonPromptResponse{
			RequestId: pr.GetRequestId(), Approve: true,
		})
	}

	client, _ = pipePair(t, answerPrompts, server)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var resp openairv1.DaemonStatusResponse
	if err := client.Do(ctx, MsgStatusRequest, &openairv1.DaemonStatusRequest{}, &resp); err != nil {
		t.Fatalf("Do: %v", err)
	}
	if resp.GetDeviceId() != "ok" {
		t.Fatalf("reply = %q, want %q", resp.GetDeviceId(), "ok")
	}
}

// TestUnknownCapIDAndMsgTypeAreIgnored is §3.1 on the local link: a newer
// client talking to an older daemon loses the feature, not the connection.
func TestUnknownCapIDAndMsgTypeAreIgnored(t *testing.T) {
	seen := make(chan uint16, 4)
	server := func(ctx context.Context, p *Peer, msgType uint16, payload []byte) {
		seen <- msgType
		if msgType == MsgStatusRequest {
			var req openairv1.DaemonStatusRequest
			_ = proto.Unmarshal(payload, &req)
			_ = p.Reply(MsgStatusResponse, req.GetRequestId(), &openairv1.DaemonStatusResponse{DeviceId: "alive"})
		}
	}
	client, _ := pipePair(t, nil, server)

	// A message type from the future.
	if err := client.Send(60000, &openairv1.DaemonStatusRequest{}); err != nil {
		t.Fatalf("send unknown msgType: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var resp openairv1.DaemonStatusResponse
	if err := client.Do(ctx, MsgStatusRequest, &openairv1.DaemonStatusRequest{}, &resp); err != nil {
		t.Fatalf("connection did not survive an unknown message type: %v", err)
	}
	if resp.GetDeviceId() != "alive" {
		t.Fatalf("reply = %q, want alive", resp.GetDeviceId())
	}
}

// TestDoFailsWhenTheConnectionDies checks that a pending call does not hang
// forever when the far end vanishes -- the failure mode a request-ID map gets
// wrong when nothing closes the waiters.
func TestDoFailsWhenTheConnectionDies(t *testing.T) {
	client, server := pipePair(t, nil, func(context.Context, *Peer, uint16, []byte) {})

	go func() {
		time.Sleep(50 * time.Millisecond)
		server.Close()
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var resp openairv1.DaemonStatusResponse
	err := client.Do(ctx, MsgStatusRequest, &openairv1.DaemonStatusRequest{}, &resp)
	if err == nil {
		t.Fatal("Do returned nil after the peer went away")
	}
	if errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("Do waited for its own deadline rather than noticing the closed connection")
	}
}

// TestSocketIsOwnerOnly is the local trust boundary D-29 names: anything that
// can open this socket drives the daemon with the identity key behind it.
func TestSocketIsOwnerOnly(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("named pipes carry an ACL instead; see transport_windows.go")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "openaird.sock")

	ln, err := Listen(path)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer ln.Close()

	st, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat socket: %v", err)
	}
	if perm := st.Mode().Perm(); perm != 0o600 {
		t.Errorf("socket mode = %o, want 600", perm)
	}
	dst, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatalf("stat socket dir: %v", err)
	}
	if perm := dst.Mode().Perm(); perm != 0o700 {
		t.Errorf("socket directory mode = %o, want 700", perm)
	}
}

// TestListenRefusesToStealALiveSocket: a stale socket file is cleaned up, but
// one that answers belongs to a running daemon and taking it would silently
// steal its clients.
func TestListenRefusesToStealALiveSocket(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no socket file on Windows")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "openaird.sock")

	first, err := Listen(path)
	if err != nil {
		t.Fatalf("first Listen: %v", err)
	}
	defer first.Close()
	go func() {
		for {
			c, err := first.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()

	if _, err := Listen(path); err == nil {
		t.Fatal("second Listen succeeded on a live socket")
	}

	first.Close()
	// The file is left behind by a listener that did not remove it; the next
	// Listen must clear it rather than refuse forever.
	if _, err := os.Stat(path); err == nil {
		second, err := Listen(path)
		if err != nil {
			t.Fatalf("Listen did not clear a stale socket: %v", err)
		}
		second.Close()
	}
}

func TestDialAndServeOverARealSocket(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("exercised through the named pipe on Windows, which needs a live daemon")
	}
	path := filepath.Join(t.TempDir(), "openaird.sock")
	ln, err := Listen(path)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer ln.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		if err := CheckPeer(c); err != nil {
			t.Errorf("CheckPeer rejected our own connection: %v", err)
			c.Close()
			return
		}
		p := NewPeer(c, func(ctx context.Context, p *Peer, msgType uint16, payload []byte) {
			var req openairv1.DaemonStatusRequest
			_ = proto.Unmarshal(payload, &req)
			_ = p.Reply(MsgStatusResponse, req.GetRequestId(), &openairv1.DaemonStatusResponse{DeviceId: "served"})
		})
		p.Serve(ctx)
	}()

	nc, err := Dial(path)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	client := NewPeer(nc, nil)
	go client.Serve(ctx)

	var resp openairv1.DaemonStatusResponse
	if err := client.Do(ctx, MsgStatusRequest, &openairv1.DaemonStatusRequest{}, &resp); err != nil {
		t.Fatalf("Do: %v", err)
	}
	if resp.GetDeviceId() != "served" {
		t.Fatalf("reply = %q, want served", resp.GetDeviceId())
	}
}

// TestEveryResponseTypeIsAReply is a schema invariant, and it exists because
// the failure it catches is silent.
//
// A response type missing from isReply is routed as an inbound request, finds no
// handler, and is dropped -- so the caller that sent the matching request waits
// for its context to expire and reports a timeout. Nothing logs, nothing
// errors, and the bug looks like a hung daemon. Adding a message pair to
// daemon.proto and forgetting this function is a one-line mistake, so it is
// checked against the schema rather than against a hand-written list.
func TestEveryResponseTypeIsAReply(t *testing.T) {
	checked := 0
	for value, name := range openairv1.DaemonMessageType_name {
		if !strings.HasSuffix(name, "_RESPONSE") {
			continue
		}
		checked++
		if !correlated(uint16(value)) {
			t.Errorf("%s is not routed by request ID: a caller sending its request would hang until its context expired", name)
		}
	}
	if checked < 8 {
		t.Fatalf("only %d response types were checked; the enum lookup is not finding them", checked)
	}

	// The converse: a request routed as a reply would never reach a handler.
	for value, name := range openairv1.DaemonMessageType_name {
		if strings.HasSuffix(name, "_REQUEST") && correlated(uint16(value)) {
			t.Errorf("%s is routed as a reply, so no handler would ever see it", name)
		}
	}
}

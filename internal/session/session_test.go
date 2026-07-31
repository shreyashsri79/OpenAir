package session

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/shreyashsri79/openair/internal/identity"
	v1 "github.com/shreyashsri79/openair/internal/wire/openair/v1"
)

const (
	msgHello = uint16(v1.ControlMessageType_CONTROL_MESSAGE_TYPE_HELLO)
	capFiles = byte(1) // CAPABILITY_ID_FILES is 2; the wire value is 1 (D-34)
	capClip  = byte(2)
)

// pair brings two sessions up over the in-memory transport, running both New
// calls concurrently -- §4 requires each side to send Hello before it reads
// one, so a sequential setup would deadlock and prove nothing.
func pair(t *testing.T, cfgA, cfgB Config) (*sess, *sess, *memTransport, *memTransport) {
	t.Helper()

	type keyed interface{ publicKey() ed25519.PublicKey }
	idA, okA := cfgA.Local.(keyed)
	idB, okB := cfgB.Local.(keyed)
	if !okA || !okB {
		t.Fatal("pair requires locals built on stubIdentity")
	}
	trA, trB := memTransportPair(idA.publicKey(), idB.publicKey())

	cfgA.Initiator = true
	cfgB.Initiator = false

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	type res struct {
		s   Session
		err error
	}
	ch := make(chan res, 2)
	go func() { s, err := newSession(ctx, trA, cfgA); ch <- res{s, err} }()
	go func() { s, err := newSession(ctx, trB, cfgB); ch <- res{s, err} }()

	var out [2]res
	for i := range out {
		select {
		case out[i] = <-ch:
		case <-ctx.Done():
			t.Fatal("timed out bringing up the session pair")
		}
	}
	for _, r := range out {
		if r.err != nil {
			t.Fatalf("newSession: %v", r.err)
		}
	}

	// The results race, so identify each side by the peer it reports.
	a, b := out[0].s.(*sess), out[1].s.(*sess)
	if a.cfg.Local != cfgA.Local {
		a, b = b, a
	}
	t.Cleanup(func() {
		_ = a.Close(uint16(CodeNoError), "test over")
		_ = b.Close(uint16(CodeNoError), "test over")
	})
	return a, b, trA, trB
}

func baseConfig(seed byte, name, platform string, handlers ...Handler) Config {
	m := map[byte]Handler{}
	for _, h := range handlers {
		m[h.CapID()] = h
	}
	return Config{
		Local:       newStubIdentity(seed),
		DisplayName: name,
		Platform:    platform,
		Handlers:    m,
	}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// --- Hello (§4) --------------------------------------------------------------

func TestHelloExchange(t *testing.T) {
	hA := newRecordingHandler(capFiles)
	hB := newRecordingHandler(capFiles)
	cfgA := baseConfig(1, "laptop", "linux", hA)
	cfgB := baseConfig(2, "phone", "android", hB)

	a, b, _, _ := pair(t, cfgA, cfgB)

	if got, want := a.Peer().DisplayName, "phone"; got != want {
		t.Errorf("A sees peer display name %q, want %q", got, want)
	}
	if got, want := a.Peer().Platform, "android"; got != want {
		t.Errorf("A sees peer platform %q, want %q", got, want)
	}
	if got, want := b.Peer().DisplayName, "laptop"; got != want {
		t.Errorf("B sees peer display name %q, want %q", got, want)
	}

	// Each side must have learned the other's DeviceID from its TLS key, not
	// from the string the peer sent.
	if got, want := a.Peer().DeviceID, cfgB.Local.DeviceID(); got != want {
		t.Errorf("A sees DeviceID %q, want %q", got, want)
	}
	if got, want := b.Peer().DeviceID, cfgA.Local.DeviceID(); got != want {
		t.Errorf("B sees DeviceID %q, want %q", got, want)
	}
	if !a.Peer().DeviceID.Valid() {
		t.Errorf("derived DeviceID %q is not well formed", a.Peer().DeviceID)
	}

	// §7.3: the tier the peer reports survives the round trip through two
	// enum orderings that run in opposite directions.
	if got, want := a.Peer().ProtectionTier, identity.TierKeystore; got != want {
		t.Errorf("A sees protection tier %v, want %v", got, want)
	}

	// Reached the way a caller outside this package must reach it.
	var sessionA Session = a
	n, ok := sessionA.(Negotiated)
	if !ok {
		t.Fatal("a Session must expose its negotiation outcome via Negotiated")
	}
	if got, want := n.ProtocolVersion(), ProtocolVersion; got != want {
		t.Errorf("effective version %d, want %d", got, want)
	}
	if _, ok := n.Capabilities()[capFiles]; !ok {
		t.Errorf("files not negotiated; got %v", n.Capabilities())
	}
	// The returned map must be a copy: a capability must not be able to edit
	// the negotiated set out from under the control loop.
	n.Capabilities()[capFiles] = 999
	if a.Capabilities()[capFiles] == 999 {
		t.Error("Capabilities returned the live map")
	}
}

// TestHelloDeviceIDMismatch is §4's "claiming an identity it cannot prove".
func TestHelloDeviceIDMismatch(t *testing.T) {
	liar := &liarIdentity{stubIdentity: newStubIdentity(3), claim: "aaaaaaaaaaaaaaaa"}
	honest := newStubIdentity(4)

	trA, trB := memTransportPair(liar.pub, honest.pub)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	errs := make(chan error, 2)
	go func() {
		_, err := newSession(ctx, trA, Config{Local: liar, Initiator: true, Handlers: map[byte]Handler{}})
		errs <- err
	}()
	go func() {
		_, err := newSession(ctx, trB, Config{Local: honest, Initiator: false, Handlers: map[byte]Handler{}})
		errs <- err
	}()

	// The honest side must reject; the liar's own New may succeed or fail
	// depending on which side wins the race to close, so only one rejection is
	// required.
	var rejected bool
	for range 2 {
		select {
		case err := <-errs:
			if code, ok := ErrorCodeOf(err); ok && code == CodeProtocolViolation {
				rejected = true
			}
		case <-ctx.Done():
			t.Fatal("timed out")
		}
	}
	if !rejected {
		t.Fatal("a Hello claiming an unprovable DeviceID was accepted")
	}
}

// TestPinnedKeyMismatch: a pinned record whose key is not the TLS key is
// KEY_MISMATCH (§10), and §10 says it must never be retried automatically.
func TestPinnedKeyMismatch(t *testing.T) {
	local := newStubIdentity(5)
	actual := newStubIdentity(6)
	other := newStubIdentity(7)

	tr, _ := memTransportPair(local.pub, actual.pub)

	_, err := newSession(context.Background(), tr, Config{
		Local:     local,
		Initiator: true,
		Peer: identity.Peer{
			DeviceID:          other.DeviceID(),
			IdentityPublicKey: other.pub,
		},
		Handlers: map[byte]Handler{},
	})
	code, ok := ErrorCodeOf(err)
	if !ok || code != CodeKeyMismatch {
		t.Fatalf("err = %v (code %v), want KEY_MISMATCH", err, code)
	}
}

// TestNonHelloBeforeHello: §4 says neither side may send anything else until it
// has both sent and received a Hello.
func TestNonHelloBeforeHello(t *testing.T) {
	local := newStubIdentity(8)
	remote := newStubIdentity(9)
	trA, trB := memTransportPair(local.pub, remote.pub)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// The rude peer opens the control stream and leads with something else.
	go func() {
		st, err := trB.OpenStream(ctx)
		if err != nil {
			return
		}
		_ = EncodeEnvelope(st, Envelope{
			Version: EnvelopeVersion,
			CapID:   0,
			MsgType: uint16(v1.ControlMessageType_CONTROL_MESSAGE_TYPE_PAIR_REQUEST),
		})
	}()

	_, err := newSession(ctx, trA, Config{Local: local, Initiator: false, Handlers: map[byte]Handler{}})
	code, ok := ErrorCodeOf(err)
	if !ok || code != CodeProtocolViolation {
		t.Fatalf("err = %v (code %v), want PROTOCOL_VIOLATION", err, code)
	}
}

// --- negotiation (§4) --------------------------------------------------------

func TestCapabilityIntersection(t *testing.T) {
	// A offers files + clipboard, B offers files only. §4: capabilities listed
	// by one peer only are silently dropped, not an error.
	a, b, _, _ := pair(t,
		baseConfig(10, "a", "linux", newRecordingHandler(capFiles), newRecordingHandler(capClip)),
		baseConfig(11, "b", "linux", newRecordingHandler(capFiles)),
	)

	if got := a.Capabilities(); len(got) != 1 {
		t.Errorf("A negotiated %v, want files only", got)
	} else if _, ok := got[capFiles]; !ok {
		t.Errorf("A negotiated %v, want files", got)
	}
	if got := b.Capabilities(); len(got) != 1 {
		t.Errorf("B negotiated %v, want files only", got)
	}
}

func TestNoSharedCapabilities(t *testing.T) {
	a, b, _, _ := pair(t,
		baseConfig(12, "a", "linux", newRecordingHandler(capFiles)),
		baseConfig(13, "b", "linux", newRecordingHandler(capClip)),
	)
	if len(a.Capabilities()) != 0 || len(b.Capabilities()) != 0 {
		t.Errorf("disjoint capability sets must negotiate to nothing, got %v / %v",
			a.Capabilities(), b.Capabilities())
	}
	// And the session is still up: a disjoint set is not an error.
	if err := a.Send(context.Background(), 0, msgHello, nil); err != nil {
		t.Errorf("session unusable after empty negotiation: %v", err)
	}
}

func TestNegotiationTakesTheLowerVersion(t *testing.T) {
	// Direct unit test of the intersection rule, since both ends of a live pair
	// necessarily advertise this build's version.
	s := &sess{handlers: map[byte]Handler{capFiles: newRecordingHandler(capFiles)}}
	got := s.negotiate([]*v1.Capability{
		{Id: v1.CapabilityId_CAPABILITY_ID_FILES, Version: 99},
		{Id: v1.CapabilityId_CAPABILITY_ID_CLIPBOARD, Version: 1},
		nil,
		{Id: v1.CapabilityId_CAPABILITY_ID_UNSPECIFIED, Version: 1},
		{Id: v1.CapabilityId(999), Version: 1},
	})
	if len(got) != 1 {
		t.Fatalf("negotiated %v, want files only", got)
	}
	if got[capFiles] != localCapVersion {
		t.Errorf("files version %d, want the lower of the two (%d)", got[capFiles], localCapVersion)
	}

	s2 := &sess{handlers: map[byte]Handler{capFiles: newRecordingHandler(capFiles)}}
	got2 := s2.negotiate([]*v1.Capability{{Id: v1.CapabilityId_CAPABILITY_ID_FILES, Version: 0}})
	if got2[capFiles] != 0 {
		t.Errorf("files version %d, want 0 -- the peer's value is the lower", got2[capFiles])
	}
}

// --- dispatch and §3.1 -------------------------------------------------------

func TestControlMessageDispatch(t *testing.T) {
	hB := newRecordingHandler(capFiles)
	a, _, _, _ := pair(t,
		baseConfig(14, "a", "linux", newRecordingHandler(capFiles)),
		baseConfig(15, "b", "linux", hB),
	)

	want := &v1.Hello{DisplayName: "payload marker"}
	if err := a.Send(context.Background(), capFiles, 3, want); err != nil {
		t.Fatal(err)
	}

	select {
	case got := <-hB.got:
		if got.MsgType != 3 {
			t.Errorf("msgType %d, want 3", got.MsgType)
		}
		var back v1.Hello
		if err := proto.Unmarshal(got.Payload, &back); err != nil {
			t.Fatalf("payload did not survive framing: %v", err)
		}
		if back.DisplayName != "payload marker" {
			t.Errorf("payload = %q", back.DisplayName)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("message never reached the handler")
	}
}

// TestUnknownCapIDIsIgnored is §3.1's first clause. The message must be skipped
// and the session must keep working -- the next message still arrives.
func TestUnknownCapIDIsIgnored(t *testing.T) {
	hB := newRecordingHandler(capFiles)
	a, b, _, trB := pair(t,
		baseConfig(16, "a", "linux", newRecordingHandler(capFiles)),
		baseConfig(17, "b", "linux", hB),
	)

	ctx := context.Background()
	// capID 200: reserved-for-experiments range, no handler, not negotiated.
	if err := a.Send(ctx, 200, 1, &v1.Hello{DisplayName: "from the future"}); err != nil {
		t.Fatal(err)
	}
	// capID 3 (remotefs): a real capID this build knows but did not negotiate.
	if err := a.Send(ctx, 3, 1, &v1.Hello{DisplayName: "also ignored"}); err != nil {
		t.Fatal(err)
	}
	// Then something the peer does handle. If the unknown ones were fatal, this
	// never arrives.
	if err := a.Send(ctx, capFiles, 4, nil); err != nil {
		t.Fatal(err)
	}

	select {
	case got := <-hB.got:
		if got.MsgType != 4 {
			t.Errorf("first delivered message was msgType %d; an ignorable one leaked through", got.MsgType)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("an unknown capID was treated as fatal")
	}
	if closed, _, _ := trB.closedWith(); closed {
		t.Error("an unknown capID closed the connection")
	}
	if b.closeErr != nil {
		t.Errorf("session recorded a failure: %v", b.closeErr)
	}
}

// TestUnknownMsgTypeIsIgnored is §3.1's second clause, in both of the places it
// can be decided: the session layer knows the control message types, and only a
// capability knows its own.
func TestUnknownMsgTypeIsIgnored(t *testing.T) {
	hB := newRecordingHandler(capFiles)
	hB.serveErr = fmt.Errorf("files: %w", ErrUnknownMsgType)
	hB.errOnType = 4242

	// A control handler on capID 0, so the control-side check is observable.
	ctrlB := newRecordingHandler(0)

	a, b, _, trB := pair(t,
		baseConfig(18, "a", "linux", newRecordingHandler(capFiles)),
		baseConfig(19, "b", "linux", hB, ctrlB),
	)

	ctx := context.Background()
	// Unknown control msgType: the session layer knows every value of
	// ControlMessageType, so this never reaches a handler.
	if err := a.Send(ctx, 0, 5000, nil); err != nil {
		t.Fatal(err)
	}
	// msgType 0 is UNSPECIFIED and never valid on the wire.
	if err := a.Send(ctx, 0, 0, nil); err != nil {
		t.Fatal(err)
	}
	// Unknown capability msgType: the capability reports it and the session
	// swallows it.
	if err := a.Send(ctx, capFiles, 4242, nil); err != nil {
		t.Fatal(err)
	}
	// A known control message, to prove the loop survived all of it.
	if err := a.Send(ctx, 0, uint16(v1.ControlMessageType_CONTROL_MESSAGE_TYPE_PATH_INFO), nil); err != nil {
		t.Fatal(err)
	}

	select {
	case got := <-ctrlB.got:
		if got.MsgType != uint16(v1.ControlMessageType_CONTROL_MESSAGE_TYPE_PATH_INFO) {
			t.Errorf("control handler saw msgType %d; an unknown one leaked through", got.MsgType)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("an unknown msgType was treated as fatal")
	}

	waitFor(t, "the capability to see its unknown msgType", func() bool { return hB.count() == 1 })
	if closed, _, _ := trB.closedWith(); closed {
		t.Error("an unknown msgType closed the connection")
	}
	if b.closeErr != nil {
		t.Errorf("session recorded a failure: %v", b.closeErr)
	}
}

// TestHandlerErrorIsNotFatal: a capability failing one message is
// operation-fatal at worst (§10). It must not take the connection down.
func TestHandlerErrorIsNotFatal(t *testing.T) {
	hB := newRecordingHandler(capFiles)
	hB.serveErr = errors.New("disk full")
	hB.errOnType = 7

	a, _, _, trB := pair(t,
		baseConfig(20, "a", "linux", newRecordingHandler(capFiles)),
		baseConfig(21, "b", "linux", hB),
	)

	ctx := context.Background()
	if err := a.Send(ctx, capFiles, 7, nil); err != nil {
		t.Fatal(err)
	}
	if err := a.Send(ctx, capFiles, 8, nil); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "both messages to be delivered", func() bool { return hB.count() == 2 })
	if closed, _, _ := trB.closedWith(); closed {
		t.Error("a handler error closed the connection")
	}
}

// TestUnknownEnvelopeVersionIsFatal is the counterweight: §3 is explicit that
// the envelope is not forward-compatible, so tolerance stops here.
func TestUnknownEnvelopeVersionIsFatal(t *testing.T) {
	a, _, _, trB := pair(t,
		baseConfig(22, "a", "linux", newRecordingHandler(capFiles)),
		baseConfig(23, "b", "linux", newRecordingHandler(capFiles)),
	)

	// Hand-written, because EncodeEnvelope refuses to emit a version it does
	// not speak -- which is itself the point of the first assertion here.
	if err := EncodeEnvelope(nil, Envelope{Version: 9}); err == nil {
		t.Error("EncodeEnvelope emitted an envelope version this build does not speak")
	}
	bad := []byte{9, 0, byte(msgHello), 0, 0, 0, 0, 0}

	a.writeMu.Lock()
	_, err := a.ctrl.Write(bad)
	a.writeMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}

	waitFor(t, "B to close the connection", func() bool {
		closed, _, _ := trB.closedWith()
		return closed
	})
	_, code, _ := trB.closedWith()
	if code != CodeProtocolViolation {
		t.Errorf("closed with %v, want PROTOCOL_VIOLATION (§3 names it, not UNKNOWN_VERSION)", code)
	}
}

func TestOversizeLengthIsFatal(t *testing.T) {
	a, _, _, trB := pair(t,
		baseConfig(24, "a", "linux", newRecordingHandler(capFiles)),
		baseConfig(25, "b", "linux", newRecordingHandler(capFiles)),
	)

	// A header claiming more than the cap, with no payload behind it.
	hdr := []byte{EnvelopeVersion, 0, 0, 0, 0, 0, 0, 0x02} // length = 0x02000000 = 32 MiB
	a.writeMu.Lock()
	_, err := a.ctrl.Write(hdr)
	a.writeMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}

	waitFor(t, "B to close the connection", func() bool {
		closed, _, _ := trB.closedWith()
		return closed
	})
	_, code, _ := trB.closedWith()
	if code != CodeMessageTooLarge {
		t.Errorf("closed with %v, want MESSAGE_TOO_LARGE", code)
	}
}

// TestPeerClosingControlStreamEndsSession is §1.1.
func TestPeerClosingControlStreamEndsSession(t *testing.T) {
	a, _, _, trB := pair(t,
		baseConfig(26, "a", "linux", newRecordingHandler(capFiles)),
		baseConfig(27, "b", "linux", newRecordingHandler(capFiles)),
	)
	_ = a.ctrl.Close()

	waitFor(t, "B to notice", func() bool {
		closed, _, _ := trB.closedWith()
		return closed
	})
	_, code, _ := trB.closedWith()
	if code != CodeNoError {
		t.Errorf("closed with %v, want NO_ERROR for a clean stream close", code)
	}
}

// --- capability streams (§3) -------------------------------------------------

func TestCapabilityStreamOpeningEnvelope(t *testing.T) {
	hB := newRecordingHandler(capFiles)
	hB.streamHook = func(st Stream) {
		_, _ = st.Write([]byte("bulk follows"))
		_ = st.Close()
	}

	a, _, _, _ := pair(t,
		baseConfig(28, "a", "linux", newRecordingHandler(capFiles)),
		baseConfig(29, "b", "linux", hB),
	)

	st, err := a.OpenStream(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// §3: the first message on every capability stream is an envelope.
	if err := EncodeEnvelope(st, Envelope{
		Version: EnvelopeVersion,
		CapID:   capFiles,
		MsgType: 11,
		Payload: []byte("stream init"),
	}); err != nil {
		t.Fatal(err)
	}

	select {
	case got := <-hB.got:
		if !got.Stream {
			t.Fatal("delivered to Serve, not ServeStream")
		}
		if got.MsgType != 11 || string(got.Payload) != "stream init" {
			t.Errorf("opening envelope = %+v", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("stream never reached the handler")
	}

	buf := make([]byte, len("bulk follows"))
	if _, err := readFull(st, buf); err != nil {
		t.Fatalf("reading the handler's reply: %v", err)
	}
	if string(buf) != "bulk follows" {
		t.Errorf("post-envelope bytes = %q", buf)
	}
}

// TestCapabilityStreamUnknownCapIsReset: §3.1 says ignore and continue, which
// for a stream means declining that stream without disturbing the connection.
func TestCapabilityStreamUnknownCapIsReset(t *testing.T) {
	a, _, _, trB := pair(t,
		baseConfig(30, "a", "linux", newRecordingHandler(capFiles)),
		baseConfig(31, "b", "linux", newRecordingHandler(capFiles)),
	)

	st, err := a.OpenStream(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := EncodeEnvelope(st, Envelope{Version: EnvelopeVersion, CapID: 200, MsgType: 1}); err != nil {
		t.Fatal(err)
	}

	ms := st.(*memStream)
	select {
	case code := <-ms.resetCode:
		if ErrorCode(code) != CodeCapabilityUnavailable {
			t.Errorf("stream reset with %v, want CAPABILITY_UNAVAILABLE", ErrorCode(code))
		}
	case <-time.After(3 * time.Second):
		t.Fatal("stream for an unknown capability was neither served nor reset")
	}
	if closed, _, _ := trB.closedWith(); closed {
		t.Error("an unknown capability stream closed the whole connection")
	}
}

// --- the rest of the Session surface -----------------------------------------

func TestSendFramesOnTheControlStream(t *testing.T) {
	local := newStubIdentity(32)
	remote := newStubIdentity(33)
	trA, trB := memTransportPair(local.pub, remote.pub)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Half a handshake: read what the initiator sends without replying, so the
	// exact bytes of its Hello can be inspected.
	got := make(chan Envelope, 1)
	go func() {
		st, err := trB.AcceptStream(ctx)
		if err != nil {
			return
		}
		env, err := DecodeEnvelope(st)
		if err != nil {
			return
		}
		got <- env
	}()

	go func() {
		_, _ = newSession(ctx, trA, Config{
			Local:       local,
			DisplayName: "sender",
			Platform:    "linux",
			Initiator:   true,
			Handlers:    map[byte]Handler{capFiles: newRecordingHandler(capFiles)},
		})
	}()

	select {
	case env := <-got:
		if env.Version != EnvelopeVersion {
			t.Errorf("ver = %d, want %d", env.Version, EnvelopeVersion)
		}
		if env.CapID != 0 {
			t.Errorf("capID = %d, want 0 (the session layer)", env.CapID)
		}
		if env.MsgType != msgHello {
			t.Errorf("msgType = %d, want HELLO (%d)", env.MsgType, msgHello)
		}
		var h v1.Hello
		if err := proto.Unmarshal(env.Payload, &h); err != nil {
			t.Fatal(err)
		}
		if h.DeviceId != string(local.DeviceID()) {
			t.Errorf("device_id = %q, want %q", h.DeviceId, local.DeviceID())
		}
		if h.ProtoVersion != ProtocolVersion {
			t.Errorf("proto_version = %d, want %d", h.ProtoVersion, ProtocolVersion)
		}
		if len(h.Capabilities) != 1 || h.Capabilities[0].Id != v1.CapabilityId_CAPABILITY_ID_FILES {
			t.Errorf("capabilities = %v, want [FILES]", h.Capabilities)
		}
		if h.ProtectionTier != v1.ProtectionTier_PROTECTION_TIER_KEYSTORE {
			t.Errorf("protection_tier = %v, want KEYSTORE", h.ProtectionTier)
		}
	case <-ctx.Done():
		t.Fatal("no Hello arrived")
	}
}

func TestSendRejectsOversizeMessage(t *testing.T) {
	a, _, _, _ := pair(t,
		baseConfig(34, "a", "linux", newRecordingHandler(capFiles)),
		baseConfig(35, "b", "linux", newRecordingHandler(capFiles)),
	)
	huge := &v1.Hello{DisplayName: string(make([]byte, MaxMessageSize+1))}
	err := a.Send(context.Background(), capFiles, 1, huge)
	code, ok := ErrorCodeOf(err)
	if !ok || code != CodeMessageTooLarge {
		t.Fatalf("err = %v (code %v), want MESSAGE_TOO_LARGE", err, code)
	}
}

func TestSendHonoursContext(t *testing.T) {
	a, _, _, _ := pair(t,
		baseConfig(36, "a", "linux", newRecordingHandler(capFiles)),
		baseConfig(37, "b", "linux", newRecordingHandler(capFiles)),
	)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := a.Send(ctx, capFiles, 1, nil); !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
}

func TestPathInfoPassesThrough(t *testing.T) {
	a, _, trA, _ := pair(t,
		baseConfig(38, "a", "linux", newRecordingHandler(capFiles)),
		baseConfig(39, "b", "linux", newRecordingHandler(capFiles)),
	)
	if got, want := a.PathInfo(), trA.path; got != want {
		t.Errorf("PathInfo = %+v, want %+v", got, want)
	}
}

// TestQuiesceIsANoOp records M1's declared gap rather than asserting behaviour:
// PROTOCOL.md §7.1 is not implemented, and the release func must still be safe
// to call so callers can be written against the final shape now.
func TestQuiesceIsANoOp(t *testing.T) {
	a, _, _, _ := pair(t,
		baseConfig(40, "a", "linux", newRecordingHandler(capFiles)),
		baseConfig(41, "b", "linux", newRecordingHandler(capFiles)),
	)
	release, err := a.Quiesce(context.Background(), 1<<20, "test")
	if err != nil {
		t.Fatalf("Quiesce: %v", err)
	}
	if release == nil {
		t.Fatal("release func is nil; callers must be able to defer it")
	}
	release()
	release() // idempotent
}

func TestCloseIsIdempotentAndCarriesTheCode(t *testing.T) {
	a, _, trA, _ := pair(t,
		baseConfig(42, "a", "linux", newRecordingHandler(capFiles)),
		baseConfig(43, "b", "linux", newRecordingHandler(capFiles)),
	)
	if err := a.Close(uint16(CodeRejected), "user declined"); err != nil {
		t.Fatal(err)
	}
	if err := a.Close(uint16(CodeNoError), "again"); err != nil {
		t.Fatal(err)
	}
	closed, code, msg := trA.closedWith()
	if !closed || code != CodeRejected || msg != "user declined" {
		t.Errorf("closed=%v code=%v msg=%q, want the first Close to win", closed, code, msg)
	}
	if _, err := a.OpenStream(context.Background()); err == nil {
		t.Error("OpenStream succeeded on a closed session")
	}
	if err := a.SendDatagram([]byte{1}); err == nil {
		t.Error("SendDatagram succeeded on a closed session")
	}
}

// --- conversions (D-34) ------------------------------------------------------

func TestCapIDConversionsAreOffsetByOne(t *testing.T) {
	cases := []struct {
		wire byte
		enum v1.CapabilityId
	}{
		{0, v1.CapabilityId_CAPABILITY_ID_CONTROL},
		{1, v1.CapabilityId_CAPABILITY_ID_FILES},
		{2, v1.CapabilityId_CAPABILITY_ID_CLIPBOARD},
		{3, v1.CapabilityId_CAPABILITY_ID_REMOTEFS},
		{4, v1.CapabilityId_CAPABILITY_ID_NOTIFICATIONS},
		{5, v1.CapabilityId_CAPABILITY_ID_INPUT},
		{6, v1.CapabilityId_CAPABILITY_ID_MIRROR},
	}
	for _, c := range cases {
		got, ok := CapIDToWire(c.enum)
		if !ok || got != c.wire {
			t.Errorf("CapIDToWire(%v) = %d,%v; want %d,true", c.enum, got, ok, c.wire)
		}
		back, ok := CapIDFromWire(c.wire)
		if !ok || back != c.enum {
			t.Errorf("CapIDFromWire(%d) = %v,%v; want %v,true", c.wire, back, ok, c.enum)
		}
	}
	if _, ok := CapIDToWire(v1.CapabilityId_CAPABILITY_ID_UNSPECIFIED); ok {
		t.Error("UNSPECIFIED has no wire value")
	}
	if _, ok := CapIDFromWire(200); ok {
		t.Error("capID 200 is not a known capability")
	}
	if _, ok := CapIDToWire(v1.CapabilityId(42)); ok {
		t.Error("an out-of-range enum has no wire value")
	}
}

// TestProtectionTierIsNotOffset guards the trap: PROTOCOL.md §7.3 numbers the
// tiers 1/2/3 and the schema matches, so this pair is NOT offset by one -- but
// identity.ProtectionTier numbers them 0/1/2 in the opposite order, so a cast
// would silently turn keystore into none.
func TestProtectionTierIsNotOffset(t *testing.T) {
	cases := []struct {
		domain identity.ProtectionTier
		wire   v1.ProtectionTier
	}{
		{identity.TierKeystore, v1.ProtectionTier_PROTECTION_TIER_KEYSTORE},
		{identity.TierPassphrase, v1.ProtectionTier_PROTECTION_TIER_PASSPHRASE},
		{identity.TierNone, v1.ProtectionTier_PROTECTION_TIER_NONE},
	}
	for _, c := range cases {
		if got := ProtectionTierToWire(c.domain); got != c.wire {
			t.Errorf("ProtectionTierToWire(%v) = %v, want %v", c.domain, got, c.wire)
		}
		if got := ProtectionTierFromWire(c.wire); got != c.domain {
			t.Errorf("ProtectionTierFromWire(%v) = %v, want %v", c.wire, got, c.domain)
		}
	}
	// A cast is the bug this table exists to catch.
	if v1.ProtectionTier(identity.TierKeystore) == v1.ProtectionTier_PROTECTION_TIER_KEYSTORE {
		t.Error("the two tier scales happen to agree; the conversion table is no longer load-bearing")
	}
	if got := ProtectionTierFromWire(v1.ProtectionTier_PROTECTION_TIER_UNSPECIFIED); got != identity.TierNone {
		t.Errorf("an unreadable tier must degrade to none, got %v", got)
	}
}

func TestTrustLevelConversions(t *testing.T) {
	cases := []struct {
		domain identity.TrustLevel
		wire   v1.TrustLevel
	}{
		{identity.LevelUnpaired, v1.TrustLevel_TRUST_LEVEL_UNPAIRED},
		{identity.LevelTrusted, v1.TrustLevel_TRUST_LEVEL_TRUSTED},
		{identity.LevelOwned, v1.TrustLevel_TRUST_LEVEL_OWNED},
	}
	for _, c := range cases {
		if got := TrustLevelToWire(c.domain); got != c.wire {
			t.Errorf("TrustLevelToWire(%v) = %v, want %v", c.domain, got, c.wire)
		}
		if got := TrustLevelFromWire(c.wire); got != c.domain {
			t.Errorf("TrustLevelFromWire(%v) = %v, want %v", c.wire, got, c.domain)
		}
		// This pair IS offset by one, unlike ProtectionTier.
		if int(c.wire) != int(c.domain)+1 {
			t.Errorf("trust levels are no longer offset by one: %v vs %v", c.domain, c.wire)
		}
	}
	if got := TrustLevelFromWire(v1.TrustLevel_TRUST_LEVEL_UNSPECIFIED); got != identity.LevelUnpaired {
		t.Errorf("unspecified trust must degrade to unpaired, got %v", got)
	}
}

// TestMsgTypeIsNotOffset: PROTOCOL.md never enumerated msgType (D-34 defect 1),
// so the schema enums are the original definition and their values are the wire
// values, with no offset.
func TestMsgTypeIsNotOffset(t *testing.T) {
	if got := MsgType(v1.ControlMessageType_CONTROL_MESSAGE_TYPE_HELLO); got != 1 {
		t.Errorf("HELLO msgType = %d, want 1", got)
	}
	if got := MsgType(v1.FilesMessageType_FILES_MESSAGE_TYPE_TRANSFER_OFFER); got != 1 {
		t.Errorf("files TRANSFER_OFFER msgType = %d, want 1", got)
	}
	// Two capabilities reusing the same msgType number is expected: msgType is
	// scoped by capID, which is why demultiplexing needs both bytes.
	if MsgType(v1.FilesMessageType_FILES_MESSAGE_TYPE_TRANSFER_OFFER) !=
		MsgType(v1.ControlMessageType_CONTROL_MESSAGE_TYPE_HELLO) {
		t.Error("msgType is capability-scoped; these are expected to collide")
	}
	if !knownControlMsgType(uint16(v1.ControlMessageType_CONTROL_MESSAGE_TYPE_PATH_INFO)) {
		t.Error("PATH_INFO must be a known control message type")
	}
	if knownControlMsgType(0) {
		t.Error("msgType 0 is UNSPECIFIED and never valid on the wire")
	}
	if knownControlMsgType(5000) {
		t.Error("msgType 5000 is not a known control message type")
	}
}

// readFull is io.ReadFull, spelled out to keep the import list of this file
// about the protocol rather than about plumbing.
func readFull(r interface{ Read([]byte) (int, error) }, buf []byte) (int, error) {
	total := 0
	for total < len(buf) {
		n, err := r.Read(buf[total:])
		total += n
		if err != nil {
			return total, err
		}
	}
	return total, nil
}

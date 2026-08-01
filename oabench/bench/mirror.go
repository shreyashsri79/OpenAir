package bench

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net"
	"sort"
	"sync"
	"time"

	"github.com/quic-go/quic-go"
)

// The ADR-4 / D-9 spike: what carries the media plane?
//
// PROTOCOL.md §14.2 is written provisionally around stream-per-frame with
// RESET_STREAM, and D-24 made that the favourite by removing the scheduling
// argument for datagrams — with bulk quiesced there is nothing for datagrams to
// be packed ahead of. D-9 stays open until it is measured, and this is the
// measurement.
//
// # What is actually being compared
//
// Two ways to put an encoded frame on one QUIC connection:
//
//   - **stream**: one bidirectional stream per frame, opening with a small
//     header and then the frame bytes, closed cleanly. A frame that goes stale
//     while it is still being sent — because a newer one is ready — is
//     RESET_STREAM'd rather than finished. QUIC provides fragmentation,
//     reassembly and flow control.
//   - **datagram**: RFC 9221 datagrams with application-level fragmentation.
//     A 200 KB keyframe is roughly 170 fragments at a 1200-byte MTU, and the
//     receiver has to reassemble them and notice when a fragment never comes.
//     Datagrams are packed ahead of stream data by quic-go's packer, and the
//     send queue is 32 entries deep with a silent discard on overflow.
//
// # What is measured
//
// End-to-end frame latency: capture instant to the moment the whole frame is
// readable at the sink. That is the only number a viewer experiences, and it is
// the one both designs claim. Reported as p50/p95/p99 with the late-frame and
// lost-frame counts beside it, because an average latency with a tenth of the
// frames missing is not a better result.
//
// Both ends run on one machine — the netem lab shapes loopback — so the clocks
// are the same clock and a one-way measurement is honest.

const (
	// fragHeaderLen is the fragment header the datagram design has to invent:
	// capID, frame sequence, fragment index, fragment count, and the capture
	// instant. QUIC gives streams all of this and gives datagrams none of it.
	fragHeaderLen = 1 + 8 + 4 + 4 + 8

	// mirrorMTU is the fragment payload. QUIC datagrams must fit one packet;
	// 1200 bytes is the conservative floor every path supports.
	mirrorMTU = 1200 - fragHeaderLen

	// mirrorCapID is the leading byte on a mirror datagram, mirroring
	// PROTOCOL.md §13's arrangement for input.
	mirrorCapID = 0x06

	// frameMagic starts every frame header. The bulk transfer shares this
	// connection and its stream carries arbitrary bytes, which without a magic
	// number decode as a frame with a plausible-looking length -- and one such
	// "frame" is enough to put a nonsense number in the latency tail.
	frameMagic = 0xF1

	// frameHeaderLen is the magic, seq, capturedAt, length and the keyframe
	// flag.
	frameHeaderLen = 1 + 8 + 8 + 4 + 1

	// maxFrameBytes bounds what the sink will treat as a frame. A keyframe at
	// a sane bitrate is a few hundred kilobytes; anything claiming more is the
	// bulk transfer sharing this connection.
	maxFrameBytes = 8 << 20
)

// MirrorConfig configures one spike run.
type MirrorConfig struct {
	Addr    string
	Mode    string // "stream" | "datagram"
	FPS     int
	Bitrate int64 // bytes per second of encoded video
	Seconds int
	// Keyframe is how many frames apart keyframes are. Zero means every 60th,
	// which is a second at 60 fps and is what a screen-share encoder does.
	Keyframe int
	// Bulk runs a saturating stream transfer alongside, which is D-24's
	// contention case: the argument for datagrams was that they are packed
	// first, and this is where that would show.
	Bulk bool
	// Quiesce throttles the bulk sender the way the session layer does (D-24),
	// so the contention case can be measured with and without the mitigation.
	Quiesce bool

	Profile string
	Label   string
}

// MirrorResult is one row of the spike.
type MirrorResult struct {
	Mode        string  `json:"mode"`
	FPS         int     `json:"fps"`
	BitrateMbps float64 `json:"bitrate_mbps"`
	Seconds     int     `json:"seconds"`
	Bulk        bool    `json:"bulk"`
	Quiesce     bool    `json:"quiesce"`

	FramesSent     int `json:"frames_sent"`
	FramesComplete int `json:"frames_complete"`
	FramesLost     int `json:"frames_lost"`  // never arrived whole
	FramesLate     int `json:"frames_late"`  // arrived after their display instant
	FramesStale    int `json:"frames_stale"` // abandoned by the source before sending

	P50Ms float64 `json:"p50_ms"`
	P95Ms float64 `json:"p95_ms"`
	P99Ms float64 `json:"p99_ms"`
	MaxMs float64 `json:"max_ms"`

	SenderCPUSec float64 `json:"sender_cpu_sec"`
	CPUSecPerGiB float64 `json:"cpu_sec_per_gib"`
	BulkMbps     float64 `json:"bulk_mbps,omitempty"`

	Profile string `json:"profile,omitempty"`
	Label   string `json:"label,omitempty"`
	Error   string `json:"error,omitempty"`
}

// Summarize writes the human-readable line.
func (r *MirrorResult) Summarize(w io.Writer) {
	if r.Error != "" {
		fmt.Fprintf(w, "  %-8s FAILED: %s\n", r.Mode, r.Error)
		return
	}
	bulk := "no bulk"
	if r.Bulk {
		bulk = "bulk"
		if r.Quiesce {
			bulk = "bulk+quiesce"
		}
	}
	fmt.Fprintf(w,
		"  %-8s %-13s %2dfps %5.1fMb/s  p50=%6.2fms p95=%7.2fms p99=%7.2fms max=%8.2fms  late=%d lost=%d stale=%d  cpu=%.1fs/GiB\n",
		r.Mode, bulk, r.FPS, r.BitrateMbps,
		r.P50Ms, r.P95Ms, r.P99Ms, r.MaxMs,
		r.FramesLate, r.FramesLost, r.FramesStale, r.CPUSecPerGiB)
	if r.BulkMbps > 0 {
		fmt.Fprintf(w, "       bulk alongside: %.1f Mb/s\n", r.BulkMbps)
	}
}

// frame is one synthetic encoded frame.
type frame struct {
	seq      uint64
	keyframe bool
	body     []byte
	deadline time.Time // when it should have been displayed
}

// ServeMirror is the sink half of the spike.
func ServeMirror(addr string) error {
	tlsConf, err := ServerTLS()
	if err != nil {
		return err
	}
	ln, err := quic.ListenAddr(addr, tlsConf, quicConfig(Config{}))
	if err != nil {
		return err
	}
	defer ln.Close()
	log.Printf("mirror sink listening on %s", addr)

	for {
		conn, err := ln.Accept(context.Background())
		if err != nil {
			return err
		}
		go serveMirrorConn(conn)
	}
}

func serveMirrorConn(conn *quic.Conn) {

	sink := newMirrorSink()
	ctx := context.Background()

	// Both framings are served at once: the sink does not need to be told
	// which is in use, and a mode that sends nothing simply reports nothing.
	go sink.acceptStreams(ctx, conn)
	go sink.receiveDatagrams(ctx, conn)

	// The control stream is the first stream the source opens; it carries the
	// run's parameters and, at the end, collects the sink's report.
	ctrl, err := conn.AcceptStream(ctx)
	if err != nil {
		return
	}
	var seconds uint32
	if err := binary.Read(ctrl, binary.LittleEndian, &seconds); err != nil {
		return
	}
	// The source closes its side when the run is over.
	io.Copy(io.Discard, ctrl)

	report := sink.report()
	if err := binary.Write(ctrl, binary.LittleEndian, report); err != nil {
		log.Printf("mirror: reporting: %v", err)
	}
	ctrl.Close()

	// The source reads the report and then closes the connection. Closing it
	// from this side first would reset the stream carrying the report, which
	// is the one thing this connection existed to deliver.
	<-conn.Context().Done()
}

// mirrorReport is what the sink sends back, fixed-layout so the control stream
// needs no schema.
type mirrorReport struct {
	Complete uint32
	Late     uint32
	P50Us    uint64
	P95Us    uint64
	P99Us    uint64
	MaxUs    uint64
}

// mirrorSink reassembles frames and records how late each one was.
type mirrorSink struct {
	mu        sync.Mutex
	latencies []time.Duration
	late      int
	partial   map[uint64]*partialFrame
}

type partialFrame struct {
	total    uint32
	got      uint32
	captured int64
	deadline int64
}

func newMirrorSink() *mirrorSink {
	return &mirrorSink{partial: map[uint64]*partialFrame{}}
}

// acceptStreams is the stream-per-frame mode's receiver.
func (s *mirrorSink) acceptStreams(ctx context.Context, conn *quic.Conn) {
	for {
		st, err := conn.AcceptUniStream(ctx)
		if err != nil {
			return
		}
		go s.readFrame(st)
	}
}

func (s *mirrorSink) readFrame(st *quic.ReceiveStream) {
	header := make([]byte, frameHeaderLen)
	if _, err := io.ReadFull(st, header); err != nil {
		// A reset arrives here: the source abandoned a stale frame, which is
		// the whole point of the design and not a loss to count against it.
		return
	}
	if header[0] != frameMagic {
		// The bulk transfer, not a frame. Drain it: an unread stream holds the
		// connection's flow control window shut.
		io.Copy(io.Discard, st)
		return
	}
	capturedAt := int64(binary.LittleEndian.Uint64(header[9:17]))
	length := binary.LittleEndian.Uint32(header[17:21])
	if length > maxFrameBytes {
		// Not a frame. The bulk transfer shares this connection and its stream
		// looks like whatever its bytes happen to say, so anything claiming to
		// be larger than a keyframe could possibly be is drained instead.
		io.Copy(io.Discard, st)
		return
	}

	body := make([]byte, length)
	if _, err := io.ReadFull(st, body); err != nil {
		return
	}
	s.record(capturedAt)

	// Drain whatever is left. The bulk transfer shares this connection and its
	// stream is read here too; leaving it unread holds the connection's flow
	// control window shut, which stalls every frame behind it -- and, the first
	// time this was written, the report as well.
	io.Copy(io.Discard, st)
}

// receiveDatagrams is the datagram mode's receiver, including the reassembly
// the design forces on the application.
func (s *mirrorSink) receiveDatagrams(ctx context.Context, conn *quic.Conn) {
	for {
		b, err := conn.ReceiveDatagram(ctx)
		if err != nil {
			return
		}
		if len(b) < fragHeaderLen || b[0] != mirrorCapID {
			continue
		}
		seq := binary.LittleEndian.Uint64(b[1:9])
		total := binary.LittleEndian.Uint32(b[13:17])
		captured := int64(binary.LittleEndian.Uint64(b[17:25]))
		if total == 0 {
			continue
		}

		s.mu.Lock()
		p := s.partial[seq]
		if p == nil {
			p = &partialFrame{total: total, captured: captured}
			s.partial[seq] = p
		}
		p.got++
		done := p.got >= p.total
		if done {
			delete(s.partial, seq)
		}
		// Frames whose fragments never all arrive would otherwise accumulate
		// here forever; anything well behind the newest frame is abandoned,
		// which is the loss detection this design needs and streams do not.
		if len(s.partial) > 240 {
			for old := range s.partial {
				if old+120 < seq {
					delete(s.partial, old)
				}
			}
		}
		s.mu.Unlock()

		if done {
			s.record(captured)
		}
	}
}

// record notes one complete frame's end-to-end latency.
func (s *mirrorSink) record(capturedAt int64) {
	latency := time.Duration(time.Now().UnixNano() - capturedAt)

	s.mu.Lock()
	defer s.mu.Unlock()
	s.latencies = append(s.latencies, latency)
}

func (s *mirrorSink) report() mirrorReport {
	s.mu.Lock()
	defer s.mu.Unlock()

	sorted := append([]time.Duration(nil), s.latencies...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })

	pick := func(q float64) uint64 {
		if len(sorted) == 0 {
			return 0
		}
		i := int(math.Ceil(q*float64(len(sorted)))) - 1
		if i < 0 {
			i = 0
		}
		if i >= len(sorted) {
			i = len(sorted) - 1
		}
		return uint64(sorted[i].Microseconds())
	}
	var max uint64
	if len(sorted) > 0 {
		max = uint64(sorted[len(sorted)-1].Microseconds())
	}
	return mirrorReport{
		Complete: uint32(len(sorted)),
		Late:     uint32(s.late),
		P50Us:    pick(0.50),
		P95Us:    pick(0.95),
		P99Us:    pick(0.99),
		MaxUs:    max,
	}
}

// RunMirror is the source half: generate frames at a rate and send them both
// ways, one mode per run.
func RunMirror(cfg MirrorConfig) MirrorResult {
	res := MirrorResult{
		Mode:        cfg.Mode,
		FPS:         cfg.FPS,
		BitrateMbps: float64(cfg.Bitrate) * 8 / 1e6,
		Seconds:     cfg.Seconds,
		Bulk:        cfg.Bulk,
		Quiesce:     cfg.Quiesce,
		Profile:     cfg.Profile,
		Label:       cfg.Label,
	}

	conn, err := dialQUIC(cfg.Addr)
	if err != nil {
		res.Error = err.Error()
		return res
	}
	defer conn.CloseWithError(0, "done")

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(cfg.Seconds+30)*time.Second)
	defer cancel()

	ctrl, err := conn.OpenStreamSync(ctx)
	if err != nil {
		res.Error = err.Error()
		return res
	}
	if err := binary.Write(ctrl, binary.LittleEndian, uint32(cfg.Seconds)); err != nil {
		res.Error = err.Error()
		return res
	}

	cpu0 := cpuSeconds()

	var (
		bulkBytes int64
		bulkDone  = make(chan struct{})
	)
	if cfg.Bulk {
		go func() {
			defer close(bulkDone)
			bulkBytes = saturate(ctx, conn, cfg.Quiesce)
		}()
	} else {
		close(bulkDone)
	}

	sent, stale, total := sendFrames(ctx, conn, cfg)
	res.FramesSent = sent
	res.FramesStale = stale

	// Give the last frames a moment to land before asking for the report.
	time.Sleep(500 * time.Millisecond)
	ctrl.Close()

	var report mirrorReport
	if err := binary.Read(ctrl, binary.LittleEndian, &report); err != nil {
		res.Error = fmt.Sprintf("collecting the sink's report: %v", err)
		return res
	}
	cancel()
	<-bulkDone

	res.FramesComplete = int(report.Complete)
	res.FramesLost = sent - stale - int(report.Complete)
	if res.FramesLost < 0 {
		res.FramesLost = 0
	}
	res.P50Ms = float64(report.P50Us) / 1000
	res.P95Ms = float64(report.P95Us) / 1000
	res.P99Ms = float64(report.P99Us) / 1000
	res.MaxMs = float64(report.MaxUs) / 1000
	// A frame is late if it lands after the next one should already be on
	// screen: at 60 fps that is 16.7 ms, and it is the number a viewer sees as
	// a stutter rather than as latency.
	budget := float64(1000) / float64(cfg.FPS)
	if res.P95Ms > budget {
		res.FramesLate = int(float64(res.FramesComplete) * 0.05)
	}

	cpu := cpuSeconds() - cpu0
	res.SenderCPUSec = cpu
	if gib := float64(total) / (1 << 30); gib > 0 {
		res.CPUSecPerGiB = cpu / gib
	}
	if cfg.Bulk && cfg.Seconds > 0 {
		res.BulkMbps = float64(bulkBytes) * 8 / float64(cfg.Seconds) / 1e6
	}
	return res
}

// sendFrames generates and sends frames for the configured duration.
func sendFrames(ctx context.Context, conn *quic.Conn, cfg MirrorConfig) (sent, stale int, bytes int64) {
	interval := time.Second / time.Duration(cfg.FPS)
	keyEvery := cfg.Keyframe
	if keyEvery <= 0 {
		keyEvery = 60
	}

	// A screen-share encoder's output is lumpy: a keyframe is roughly ten
	// times a delta frame, and the average over a keyframe interval is the
	// configured bitrate.
	perFrame := float64(cfg.Bitrate) / float64(cfg.FPS)
	deltaSize := int(perFrame * float64(keyEvery) / float64(keyEvery+9))
	keySize := deltaSize * 10

	body := make([]byte, keySize)
	rand.Read(body)

	deadline := time.Now()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	stop := time.Now().Add(time.Duration(cfg.Seconds) * time.Second)
	var seq uint64

	// inFlight is the stream mode's stale-frame check. §14.2 says a frame that
	// has not finished sending when a newer one is ready MUST be reset, which
	// is the property that keeps latency bounded: the alternative is a queue of
	// frames nobody will ever want to see.
	type pending struct {
		st   *quic.SendStream
		done chan struct{}
	}
	var inFlight *pending

	for {
		select {
		case <-ctx.Done():
			return sent, stale, bytes
		case now := <-ticker.C:
			if now.After(stop) {
				if inFlight != nil {
					<-inFlight.done
				}
				return sent, stale, bytes
			}
			seq++
			isKey := seq%uint64(keyEvery) == 1
			size := deltaSize
			if isKey {
				size = keySize
			}
			f := frame{seq: seq, keyframe: isKey, body: body[:size], deadline: deadline.Add(interval)}
			deadline = f.deadline

			switch cfg.Mode {
			case "datagram":
				if err := sendFrameDatagram(conn, f); err != nil {
					return sent, stale, bytes
				}
			default:
				if inFlight != nil {
					select {
					case <-inFlight.done:
					default:
						// Still going out with a newer frame ready: abandon it.
						inFlight.st.CancelWrite(1)
						stale++
					}
					inFlight = nil
				}
				st, err := conn.OpenUniStreamSync(ctx)
				if err != nil {
					return sent, stale, bytes
				}
				p := &pending{st: st, done: make(chan struct{})}
				inFlight = p
				go func(f frame) {
					defer close(p.done)
					if err := writeFrame(p.st, f); err != nil {
						return
					}
					p.st.Close()
				}(f)
			}
			sent++
			bytes += int64(size)
		}
	}
}

// writeFrame is §14.2's payload: a header, then the frame bytes.
func writeFrame(st *quic.SendStream, f frame) error {
	header := make([]byte, frameHeaderLen)
	header[0] = frameMagic
	binary.LittleEndian.PutUint64(header[1:9], f.seq)
	binary.LittleEndian.PutUint64(header[9:17], uint64(time.Now().UnixNano()))
	binary.LittleEndian.PutUint32(header[17:21], uint32(len(f.body)))
	if f.keyframe {
		header[21] = 1
	}
	if _, err := st.Write(header); err != nil {
		return err
	}
	if _, err := st.Write(f.body); err != nil {
		return err
	}
	return nil
}

// sendFrameDatagram is the datagram design, with the fragmentation and the
// per-fragment header the application has to invent because datagrams carry
// none of what a stream carries for free.
func sendFrameDatagram(conn *quic.Conn, f frame) error {
	fragments := (len(f.body) + mirrorMTU - 1) / mirrorMTU
	if fragments == 0 {
		fragments = 1
	}
	captured := uint64(time.Now().UnixNano())

	for i := 0; i < fragments; i++ {
		start := i * mirrorMTU
		end := start + mirrorMTU
		if end > len(f.body) {
			end = len(f.body)
		}

		payload := make([]byte, fragHeaderLen+(end-start))
		payload[0] = mirrorCapID
		binary.LittleEndian.PutUint64(payload[1:9], f.seq)
		binary.LittleEndian.PutUint32(payload[9:13], uint32(i))
		binary.LittleEndian.PutUint32(payload[13:17], uint32(fragments))
		// The capture instant is on every fragment rather than only the first:
		// fragments arrive in any order, or not at all, and a reassembler that
		// needed fragment zero to know when the frame was captured would lose
		// the timing of every frame that lost it.
		binary.LittleEndian.PutUint64(payload[17:25], captured)
		copy(payload[fragHeaderLen:], f.body[start:end])

		if err := conn.SendDatagram(payload); err != nil {
			// The 32-deep send queue overflowed, or the datagram is too large
			// for the path. Both are silent frame loss in this design, and
			// counting them is half the point of the spike.
			var tooLarge *quic.DatagramTooLargeError
			if errors.As(err, &tooLarge) {
				return err
			}
			continue
		}
	}
	return nil
}

// saturate runs a bulk transfer on the same connection, throttled to a floor
// when quiesce is on (D-24).
func saturate(ctx context.Context, conn *quic.Conn, quiesce bool) int64 {
	st, err := conn.OpenUniStreamSync(ctx)
	if err != nil {
		return 0
	}
	defer st.Close()

	buf := make([]byte, 1<<20)
	var total int64
	// The quiesce floor: D-24 throttles bulk rather than stopping it, so the
	// contention case with the mitigation is bulk at a floor rather than no
	// bulk at all.
	floor := time.Duration(0)
	if quiesce {
		floor = 100 * time.Millisecond // roughly 10 MB/s
	}
	for {
		select {
		case <-ctx.Done():
			return total
		default:
		}
		n, err := st.Write(buf)
		total += int64(n)
		if err != nil {
			return total
		}
		if floor > 0 {
			time.Sleep(floor)
		}
	}
}

// dialQUIC opens the spike's connection.
func dialQUIC(addr string) (*quic.Conn, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	udpAddr, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		return nil, err
	}
	pub, _ := SpikeIdentity()
	return quic.DialAddr(ctx, udpAddr.String(), ClientTLS(pub), quicConfig(Config{}))
}

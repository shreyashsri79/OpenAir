package bench

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"log"
	"os"
	"sync"
	"time"

	"github.com/quic-go/quic-go"
)

// GSOState reports whether quic-go's generic segmentation offload is active.
// Disabling it via QUIC_GO_DISABLE_GSO is how a Linux box approximates the
// Windows send path, which has no equivalent batching -- see README.
func GSOState() string {
	if os.Getenv("QUIC_GO_DISABLE_GSO") != "" {
		return "off"
	}
	return "on"
}

func quicConfig(cfg Config) *quic.Config {
	streamWnd := cfg.StreamWindow
	if streamWnd == 0 {
		streamWnd = 16 << 20
	}
	connWnd := cfg.ConnWindow
	if connWnd == 0 {
		connWnd = 64 << 20
	}
	return &quic.Config{
		MaxIdleTimeout:                 60 * time.Second,
		KeepAlivePeriod:                5 * time.Second,
		InitialStreamReceiveWindow:     streamWnd,
		MaxStreamReceiveWindow:         streamWnd,
		InitialConnectionReceiveWindow: connWnd,
		MaxConnectionReceiveWindow:     connWnd,
		MaxIncomingStreams:             1024,
		EnableDatagrams:                true,
	}
}

// datagramPing measures round trip over RFC 9221 datagrams. Datagrams skip
// stream flow control but are still paced by the connection's congestion
// controller, which is exactly why this number matters: it shows what a
// standing queue costs traffic that is supposed to be latency-sensitive.
func datagramPing(conn *quic.Conn) pingFunc {
	buf := make([]byte, probePayload)
	var seq uint64
	return func() (time.Duration, error) {
		seq++
		binary.LittleEndian.PutUint64(buf, seq)
		start := time.Now()
		if err := conn.SendDatagram(buf); err != nil {
			return 0, err
		}
		ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
		defer cancel()
		for {
			data, err := conn.ReceiveDatagram(ctx)
			if err != nil {
				if ctx.Err() != nil {
					return 0, errProbeLost
				}
				return 0, err
			}
			// A late echo from an exchange that already timed out must not be
			// credited to this one; keep reading until the sequence matches.
			if len(data) >= probePayload && binary.LittleEndian.Uint64(data) == seq {
				return time.Since(start), nil
			}
		}
	}
}

func echoDatagrams(conn *quic.Conn) {
	for {
		data, err := conn.ReceiveDatagram(context.Background())
		if err != nil {
			return
		}
		if err := conn.SendDatagram(data); err != nil {
			return
		}
	}
}

// ServeQUIC is the v2-shaped receiver: a single QUIC connection per peer with
// every channel multiplexed over it, exactly as the HLD's "one connection per
// peer" principle requires.
func ServeQUIC(addr, sinkPath string) error {
	tlsConf, err := ServerTLS()
	if err != nil {
		return err
	}
	pub, _ := SpikeIdentity()

	ln, err := quic.ListenAddr(addr, tlsConf, quicConfig(Config{}))
	if err != nil {
		return err
	}
	defer ln.Close()
	log.Printf("quic receiver listening on %s (device %s, gso=%s)", ln.Addr(), DeviceID(pub), GSOState())

	for {
		conn, err := ln.Accept(context.Background())
		if err != nil {
			return err
		}
		go func(c *quic.Conn) {
			if err := serveQUICSession(c, sinkPath); err != nil {
				log.Printf("quic session: %v", err)
			}
			c.CloseWithError(0, "done")
		}(conn)
	}
}

func serveQUICSession(conn *quic.Conn, sinkPath string) error {
	ctx := context.Background()

	ctrl, err := conn.AcceptStream(ctx)
	if err != nil {
		return fmt.Errorf("accept control stream: %w", err)
	}
	var role [1]byte
	if _, err := io.ReadFull(ctrl, role[:]); err != nil {
		return err
	}
	if role[0] != roleControl {
		return fmt.Errorf("first stream had role %d, want control", role[0])
	}

	p, err := readPreamble(ctrl)
	if err != nil {
		return fmt.Errorf("read preamble: %w", err)
	}
	log.Printf("quic session: %s over %d streams, %s chunks",
		HumanBytes(p.TotalBytes), p.Streams, HumanBytes(int64(p.ChunkBytes)))

	sk, err := newSink(p.TotalBytes, sinkPath)
	if err != nil {
		return err
	}
	defer sk.close()

	if p.probing() {
		ping, err := conn.AcceptStream(ctx)
		if err != nil {
			return fmt.Errorf("accept ping stream: %w", err)
		}
		var r [1]byte
		if _, err := io.ReadFull(ping, r[:]); err != nil || r[0] != rolePing {
			return fmt.Errorf("expected ping stream, got role %d", r[0])
		}
		go echoStream(ping)
		go echoDatagrams(conn)
	}

	if _, err := ctrl.Write([]byte{1}); err != nil {
		return err
	}

	var wg sync.WaitGroup
	for i := int32(0); i < p.Streams; i++ {
		st, err := conn.AcceptStream(ctx)
		if err != nil {
			return err
		}
		var r [1]byte
		if _, err := io.ReadFull(st, r[:]); err != nil || r[0] != roleData {
			continue
		}
		wg.Add(1)
		go func(s *quic.Stream) {
			defer wg.Done()
			buf := make([]byte, int(p.ChunkBytes)+HeaderSize)
			if err := sk.recvChunks(s, buf); err != nil {
				log.Printf("quic recv: %v", err)
			}
		}(st)
	}

	<-sk.wait()
	if _, err := ctrl.Write([]byte{1}); err != nil {
		return err
	}
	wg.Wait()
	log.Printf("quic session complete: %s", HumanBytes(sk.count.Load()))

	// Wait for the sender to close the control stream before returning, since
	// the caller closes the connection on return. CONNECTION_CLOSE preempts
	// undelivered stream data, so tearing down here would race the ack away
	// and the sender would never stop its clock.
	io.Copy(io.Discard, ctrl)
	return nil
}

// RunQUIC performs one transfer over a single QUIC connection carrying N
// parallel streams. Streams=1 is the "does one QUIC stream saturate the link"
// case; higher values test whether v1.0's parallelism trick still buys
// anything once congestion control is shared across one connection.
func RunQUIC(cfg Config) *Result {
	res := &Result{
		Transport: "quic", Streams: cfg.Streams, ChunkBytes: cfg.ChunkBytes,
		TotalBytes: cfg.TotalBytes, GSO: GSOState(),
		Profile: cfg.Profile, Label: cfg.Label,
	}
	ctx := context.Background()
	pub, _ := SpikeIdentity()

	setupStart := time.Now()
	conn, err := quic.DialAddr(ctx, cfg.Addr, ClientTLS(pub), quicConfig(cfg))
	if err != nil {
		res.Error = fmt.Sprintf("dial: %v", err)
		return res
	}
	defer conn.CloseWithError(0, "done")

	ctrl, err := conn.OpenStreamSync(ctx)
	if err != nil {
		res.Error = err.Error()
		return res
	}
	if _, err := ctrl.Write([]byte{roleControl}); err != nil {
		res.Error = err.Error()
		return res
	}
	if err := writePreamble(ctrl, preamble{
		TotalBytes: cfg.TotalBytes,
		Streams:    int32(cfg.Streams),
		ChunkBytes: int32(cfg.ChunkBytes),
		Flags:      probeFlag(cfg.Probe),
	}); err != nil {
		res.Error = err.Error()
		return res
	}
	var pingStream *quic.Stream
	if cfg.Probe {
		pingStream, err = conn.OpenStreamSync(ctx)
		if err != nil {
			res.Error = fmt.Sprintf("open ping stream: %v", err)
			return res
		}
		if _, err := pingStream.Write([]byte{rolePing}); err != nil {
			res.Error = err.Error()
			return res
		}
	}

	var one [1]byte
	if _, err := io.ReadFull(ctrl, one[:]); err != nil {
		res.Error = fmt.Sprintf("await go: %v", err)
		return res
	}

	streams := make([]*quic.Stream, 0, cfg.Streams)
	for i := 0; i < cfg.Streams; i++ {
		st, err := conn.OpenStreamSync(ctx)
		if err != nil {
			res.Error = fmt.Sprintf("open stream %d: %v", i, err)
			return res
		}
		if _, err := st.Write([]byte{roleData}); err != nil {
			res.Error = err.Error()
			return res
		}
		streams = append(streams, st)
	}
	res.SetupSec = time.Since(setupStart).Seconds()

	var pings map[string]pingFunc
	if cfg.Probe {
		pings = map[string]pingFunc{
			"quic-stream":   streamPing(pingStream),
			"quic-datagram": datagramPing(conn),
		}
		res.Probes = append(res.Probes, probeIdle(pings)...)
	}

	plan := newChunkPlan(cfg.TotalBytes, cfg.ChunkBytes)
	src := payload(int(cfg.ChunkBytes))

	busyStop, busyDone := startBusyProbes(pings)

	cpuStart := cpuSeconds()
	transferStart := time.Now()

	var wg sync.WaitGroup
	errs := make(chan error, len(streams))
	for _, st := range streams {
		wg.Add(1)
		go func(s *quic.Stream) {
			defer wg.Done()
			buf := make([]byte, cfg.ChunkBytes+HeaderSize)
			if err := sendChunks(s, plan, buf, src); err != nil {
				errs <- err
				return
			}
			s.Close() // closes the send direction, signalling EOF to the peer
		}(st)
	}
	wg.Wait()
	close(errs)
	if err := <-errs; err != nil {
		res.Error = err.Error()
		return res
	}

	if _, err := io.ReadFull(ctrl, one[:]); err != nil {
		res.Error = fmt.Sprintf("await completion: %v", err)
		return res
	}
	res.TransferSec = time.Since(transferStart).Seconds()
	res.finalize(cpuSeconds() - cpuStart)
	res.Probes = append(res.Probes, finishBusyProbes(busyStop, busyDone)...)

	ctrl.Close() // releases the receiver, which is draining this stream to EOF
	return res
}

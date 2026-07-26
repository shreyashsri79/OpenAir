package bench

import (
	"fmt"
	"io"
	"log"
	"net"
	"sync"
	"time"
)

// ServeTCP is the v1.0-shaped receiver: one listener, N independent TCP
// connections per transfer, chunks reconstructed by offset.
func ServeTCP(addr, sinkPath string) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	defer ln.Close()
	log.Printf("tcp receiver listening on %s", ln.Addr())

	for {
		conn, err := ln.Accept()
		if err != nil {
			return err
		}
		var role [1]byte
		if _, err := io.ReadFull(conn, role[:]); err != nil || role[0] != roleControl {
			conn.Close()
			continue
		}
		if err := serveTCPSession(ln, conn, sinkPath); err != nil {
			log.Printf("tcp session: %v", err)
		}
		conn.Close()
	}
}

func serveTCPSession(ln net.Listener, ctrl net.Conn, sinkPath string) error {
	p, err := readPreamble(ctrl)
	if err != nil {
		return fmt.Errorf("read preamble: %w", err)
	}
	log.Printf("tcp session: %s over %d connections, %s chunks",
		HumanBytes(p.TotalBytes), p.Streams, HumanBytes(int64(p.ChunkBytes)))

	sk, err := newSink(p.TotalBytes, sinkPath)
	if err != nil {
		return err
	}
	defer sk.close()

	if p.probing() {
		conn, err := ln.Accept()
		if err != nil {
			return err
		}
		var role [1]byte
		if _, err := io.ReadFull(conn, role[:]); err != nil || role[0] != rolePing {
			conn.Close()
			return fmt.Errorf("expected ping connection, got role %d", role[0])
		}
		go func() {
			defer conn.Close()
			echoStream(conn)
		}()
	}

	// Tell the sender to dial its data connections. Gating on this means a
	// single accept loop can attribute connections without racing.
	if _, err := ctrl.Write([]byte{1}); err != nil {
		return err
	}

	var wg sync.WaitGroup
	for i := int32(0); i < p.Streams; i++ {
		conn, err := ln.Accept()
		if err != nil {
			return err
		}
		var role [1]byte
		if _, err := io.ReadFull(conn, role[:]); err != nil || role[0] != roleData {
			conn.Close()
			continue
		}
		wg.Add(1)
		go func(c net.Conn) {
			defer wg.Done()
			defer c.Close()
			buf := make([]byte, int(p.ChunkBytes)+HeaderSize)
			if err := sk.recvChunks(c, buf); err != nil {
				log.Printf("tcp recv: %v", err)
			}
		}(conn)
	}

	<-sk.wait()
	if _, err := ctrl.Write([]byte{1}); err != nil { // completion ack, stops the sender's clock
		return err
	}
	wg.Wait()
	log.Printf("tcp session complete: %s", HumanBytes(sk.count.Load()))
	return nil
}

// RunTCP performs one parallel-TCP transfer and reports its result. This is the
// baseline the QUIC candidate has to beat, so it gets every fair advantage:
// no per-chunk logging, one write syscall per chunk, kernel socket buffer
// autotuning left alone.
func RunTCP(cfg Config) *Result {
	res := &Result{
		Transport: "tcp", Streams: cfg.Streams, ChunkBytes: cfg.ChunkBytes,
		TotalBytes: cfg.TotalBytes, GSO: "n/a",
		Profile: cfg.Profile, Label: cfg.Label,
	}

	setupStart := time.Now()
	ctrl, err := net.Dial("tcp", cfg.Addr)
	if err != nil {
		res.Error = fmt.Sprintf("dial control: %v", err)
		return res
	}
	defer ctrl.Close()

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

	// The probe rides its own connection here, mirroring v1.0's architecture
	// where control never shared a socket with bulk. That asymmetry against
	// QUIC's shared connection is the comparison, not a flaw in it.
	var pingConn net.Conn
	if cfg.Probe {
		pingConn, err = net.Dial("tcp", cfg.Addr)
		if err != nil {
			res.Error = fmt.Sprintf("dial ping: %v", err)
			return res
		}
		defer pingConn.Close()
		if _, err := pingConn.Write([]byte{rolePing}); err != nil {
			res.Error = err.Error()
			return res
		}
	}

	var one [1]byte
	if _, err := io.ReadFull(ctrl, one[:]); err != nil {
		res.Error = fmt.Sprintf("await go: %v", err)
		return res
	}

	conns := make([]net.Conn, 0, cfg.Streams)
	defer func() {
		for _, c := range conns {
			c.Close()
		}
	}()
	for i := 0; i < cfg.Streams; i++ {
		c, err := net.Dial("tcp", cfg.Addr)
		if err != nil {
			res.Error = fmt.Sprintf("dial data %d: %v", i, err)
			return res
		}
		if _, err := c.Write([]byte{roleData}); err != nil {
			res.Error = err.Error()
			return res
		}
		conns = append(conns, c)
	}
	res.SetupSec = time.Since(setupStart).Seconds()

	var pings map[string]pingFunc
	if cfg.Probe {
		pings = map[string]pingFunc{"tcp-sepconn": streamPing(pingConn)}
		res.Probes = append(res.Probes, probeIdle(pings)...)
	}

	plan := newChunkPlan(cfg.TotalBytes, cfg.ChunkBytes)
	src := payload(int(cfg.ChunkBytes))

	busyStop, busyDone := startBusyProbes(pings)

	cpuStart := cpuSeconds()
	transferStart := time.Now()

	var wg sync.WaitGroup
	errs := make(chan error, len(conns))
	for _, c := range conns {
		wg.Add(1)
		go func(c net.Conn) {
			defer wg.Done()
			buf := make([]byte, cfg.ChunkBytes+HeaderSize)
			if err := sendChunks(c, plan, buf, src); err != nil {
				errs <- err
				return
			}
			if tc, ok := c.(*net.TCPConn); ok {
				tc.CloseWrite()
			}
		}(c)
	}
	wg.Wait()
	close(errs)
	if err := <-errs; err != nil {
		res.Error = err.Error()
		return res
	}

	// Block until the receiver confirms it holds every byte. Without this the
	// clock would stop when the last write hit the kernel send buffer, not when
	// the data actually landed.
	if _, err := io.ReadFull(ctrl, one[:]); err != nil {
		res.Error = fmt.Sprintf("await completion: %v", err)
		return res
	}
	res.TransferSec = time.Since(transferStart).Seconds()
	res.finalize(cpuSeconds() - cpuStart)
	res.Probes = append(res.Probes, finishBusyProbes(busyStop, busyDone)...)
	return res
}

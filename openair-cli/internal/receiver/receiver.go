package receiver

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/grandcat/zeroconf"
)

const (
	PORT        = 9000
	ServiceType = "_openair._tcp"
	Domain      = "local."
	ansiReset   = "\x1b[0m"
	ansiGreen   = "\x1b[32m"
	ansiRed     = "\x1b[31m"
	ansiCyan    = "\x1b[36m"
	ansiBold    = "\x1b[1m"

	// Read buffer per data connection — must be >= largest chunk Android sends.
	// 8 MB gives the bufio.Reader enough room to batch syscalls aggressively.
	readBufSize = 8 * 1024 * 1024
)

// ---------- COLOR ----------
func greenf(format string, a ...any) string { return ansiGreen + fmt.Sprintf(format, a...) + ansiReset }
func redf(format string, a ...any) string   { return ansiRed + fmt.Sprintf(format, a...) + ansiReset }
func cyanf(format string, a ...any) string  { return ansiCyan + fmt.Sprintf(format, a...) + ansiReset }
func boldf(format string, a ...any) string  { return ansiBold + fmt.Sprintf(format, a...) + ansiReset }

// ---------- STRUCTS ----------
type Meta struct {
	Name      string `json:"name"`
	Size      int64  `json:"size"`
	Timestamp int64  `json:"timestamp"`
}

type ReceiverMeta struct {
	Timestamp int64  `json:"timestamp"`
	Data      string `json:"data"`
}

// ---------- TRANSFER STATE ----------
// One transferState per in-flight file. Passed by pointer to every goroutine
// that handles a data connection for this transfer. No globals.
type transferState struct {
	meta          Meta
	file          *os.File
	bytesReceived atomic.Int64 // updated lock-free; WriteAt is concurrent-safe
	transferStart time.Time
	once          sync.Once // ensures printBenchmark runs exactly once
}

// ---------- GLOBAL SESSION ----------
// Guards the handshake → data pairing. Only one transfer at a time for now.
var (
	sessionMu  sync.Mutex
	currentXfr *transferState
)

// ---------- MAIN ----------
func RunReceiver() error {
	fmt.Println(greenf("Starting OpenAir Receiver..."))

	mdns, err := zeroconf.Register(
		"OpenAir-Receiver", ServiceType, Domain, PORT,
		[]string{"app=OpenAir", "ver=1"}, nil,
	)
	if err != nil {
		return fmt.Errorf("mDNS error: %w", err)
	}
	defer mdns.Shutdown()
	fmt.Println(greenf("mDNS active on port %d", PORT))

	ln, err := net.Listen("tcp", fmt.Sprintf(":%d", PORT))
	if err != nil {
		return fmt.Errorf("failed to listen: %w", err)
	}
	defer ln.Close()
	fmt.Println(greenf("Listening on port %d", PORT))

	// Accept loop — every connection (control or data) gets its own goroutine.
	for {
		conn, err := ln.Accept()
		if err != nil {
			continue
		}
		go handleConnection(conn)
	}
}

// ---------- ROUTER ----------
func handleConnection(conn net.Conn) {
	defer conn.Close()

	// Large read buffer so the kernel fills it in one recv() call.
	// We peek one byte to decide control vs data without consuming it.
	reader := bufio.NewReaderSize(conn, readBufSize)

	peek, err := reader.Peek(1)
	if err != nil {
		return
	}

	if peek[0] == '{' {
		handleControl(conn, reader)
	} else {
		handleData(conn, reader)
	}
}

// ---------- CONTROL ----------
func handleControl(conn net.Conn, reader *bufio.Reader) {
	fmt.Println(greenf("Control connection"))

	line, err := reader.ReadString('\n')
	if err != nil {
		return
	}

	var meta Meta
	if err = json.Unmarshal([]byte(strings.TrimSpace(line)), &meta); err != nil {
		fmt.Println(redf("Invalid metadata: %v", err))
		return
	}
	fmt.Println(greenf("Incoming: %s (%s)", meta.Name, formatBytes(meta.Size)))

	receiveDir := getReceiverDir()
	savePath := filepath.Join(receiveDir, meta.Name)
	f, err := os.Create(savePath)
	if err != nil {
		fmt.Println(redf("File creation failed: %v", err))
		conn.Write([]byte("REJECT\n"))
		return
	}
	// Pre-allocate the full file so concurrent WriteAt calls never race to
	// extend the file — pwrite(2) on a pre-allocated file is safe.
	if err = f.Truncate(meta.Size); err != nil {
		fmt.Println(redf("Truncate failed: %v", err))
		f.Close()
		conn.Write([]byte("REJECT\n"))
		return
	}

	xfr := &transferState{
		meta:          meta,
		file:          f,
		transferStart: time.Now(),
	}

	sessionMu.Lock()
	currentXfr = xfr
	sessionMu.Unlock()

	conn.Write([]byte("ACCEPT\n"))

	// Send probe ACK with actual file size so Android's BW estimate is
	// at least in the right order of magnitude (size / RTT).
	resp := ReceiverMeta{
		Timestamp: time.Now().UnixMilli(),
		Data:      fmt.Sprintf("ACK:%d", meta.Size),
	}
	js, _ := json.Marshal(resp)
	conn.Write(append(js, '\n'))

	fmt.Println(greenf("Handshake done — workers may connect"))
}

// ---------- DATA ----------
func handleData(conn net.Conn, reader *bufio.Reader) {
	sessionMu.Lock()
	xfr := currentXfr
	sessionMu.Unlock()

	if xfr == nil {
		fmt.Println(redf("Data connection before handshake — dropping"))
		return
	}

	fmt.Println(cyanf("Worker connected"))

	// Reuse one chunk buffer per goroutine — no per-chunk allocation / GC.
	// Sized to the maximum chunk Android can send (DEFAULT_CHUNK = 4 MB).
	chunkBuf := make([]byte, 4*1024*1024)

	for {
		// Read the full 12-byte header in one call instead of two binary.Read
		// calls. io.ReadFull goes through the bufio buffer — single copy, no
		// extra syscall.
		var header [12]byte
		if _, err := io.ReadFull(reader, header[:]); err != nil {
			if err != io.EOF {
				fmt.Println(redf("Header read error: %v", err))
			}
			break
		}

		offset := int64(binary.LittleEndian.Uint64(header[0:8]))
		size := int32(binary.LittleEndian.Uint32(header[8:12]))

		if size <= 0 || int(size) > len(chunkBuf) {
			fmt.Println(redf("Invalid chunk size: %d", size))
			break
		}

		buf := chunkBuf[:size]
		if _, err := io.ReadFull(reader, buf); err != nil {
			fmt.Println(redf("Payload read error: %v", err))
			break
		}

		// WriteAt is concurrency-safe on *os.File (maps to pwrite(2)) —
		// no mutex needed even with 4 parallel worker goroutines.
		if _, err := xfr.file.WriteAt(buf, offset); err != nil {
			fmt.Println(redf("WriteAt error: %v", err))
			break
		}

		received := xfr.bytesReceived.Add(int64(size))
		if received >= xfr.meta.Size {
			xfr.once.Do(func() { printBenchmark(xfr) })
			break
		}
	}
}

// ---------- BENCHMARK ----------
func printBenchmark(xfr *transferState) {
	xfr.file.Sync()
	xfr.file.Close()

	elapsed := time.Since(xfr.transferStart)
	var throughputMbps float64 // megabits per second
	if elapsed.Seconds() > 0 {
		throughputMbps = float64(xfr.meta.Size) * 8 / elapsed.Seconds() / 1_000_000
	}

	fmt.Println()
	fmt.Println(boldf("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"))
	fmt.Println(boldf("       TRANSFER BENCHMARK"))
	fmt.Println(boldf("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"))
	fmt.Printf("  File      : %s\n", xfr.meta.Name)
	fmt.Printf("  Size      : %s\n", formatBytes(xfr.meta.Size))
	fmt.Printf("  Duration  : %s\n", cyanf("%d ms", elapsed.Milliseconds()))
	fmt.Printf("  Throughput: %s\n", cyanf("%.2f Mb/s", throughputMbps))
	fmt.Println(boldf("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"))
	fmt.Println(greenf("✓ Transfer complete → %s", filepath.Join(getReceiverDir(), xfr.meta.Name)))
	fmt.Println()

	sessionMu.Lock()
	if currentXfr == xfr {
		currentXfr = nil
	}
	sessionMu.Unlock()
}

// ---------- UTILS ----------
func formatBytes(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.2f %cB", float64(b)/float64(div), "KMGTPE"[exp])
}

func getReceiverDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "."
	}

	openairDir := filepath.Join(home, "OpenAir")
	if err := os.MkdirAll(openairDir, 0755); err == nil {
		return openairDir
	}

	downloadsDir := filepath.Join(home, "Downloads")
	if err := os.MkdirAll(downloadsDir, 0755); err == nil {
		return downloadsDir
	}

	return "."
}
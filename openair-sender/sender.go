package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/shreyashsri79/openair-sender/internal"
	"github.com/shreyashsri79/openair-sender/internal/discovery"
	"github.com/shreyashsri79/openair-sender/internal/models"
)

const (
	ansiReset = "\x1b[0m"
	ansiGreen = "\x1b[32m"
	ansiRed   = "\x1b[31m"
)

func greenf(format string, a ...any) string {
	return ansiGreen + fmt.Sprintf(format, a...) + ansiReset
}

func redf(format string, a ...any) string {
	return ansiRed + fmt.Sprintf(format, a...) + ansiReset
}

type Meta struct {
	Name      string `json:"name"`
	Size      int64  `json:"size"`
	Timestamp int64  `json:"timestamp"`
}

type ReceiverMeta struct {
	Timestamp int64  `json:"timestamp"`
	Data      string `json:"data"`
}

func main() {
	if len(os.Args) < 2 {
		fmt.Println(redf("Usage: go run sender.go <file_path>"))
		os.Exit(1)
	}

	filePath := os.Args[1]

	// Discover receiver
	host, port, err := discovery.DiscoverAndroid()
	if err != nil {
		fmt.Println(redf("Discovery error: %v", err))
		os.Exit(1)
	}
	targetAddr := fmt.Sprintf("%s:%d", host, port)

	// Open file
	file, err := os.Open(filePath)
	if err != nil {
		fmt.Println(redf("File open error: %v", err))
		os.Exit(1)
	}
	defer file.Close()

	fileInfo, _ := file.Stat()

	// Prepare metadata
	meta := Meta{
		Name:      filepath.Base(filePath),
		Size:      fileInfo.Size(),
		Timestamp: time.Now().Unix(),
	}

	// Control connection (ONLY for handshake)
	conn, err := net.DialTimeout("tcp", targetAddr, 5*time.Second)
	if err != nil {
		fmt.Println(redf("Connection failed: %v", err))
		os.Exit(1)
	}
	defer conn.Close()

	fmt.Println(greenf("Connected to receiver"))

	// Send metadata
	header, _ := json.Marshal(meta)
	conn.Write(append(header, '\n'))

	reader := bufio.NewReader(conn)

	// Wait for ACCEPT
	response, err := reader.ReadString('\n')
	if err != nil || !strings.Contains(strings.ToUpper(response), "ACCEPT") {
		fmt.Println(redf("Receiver rejected: %v", response))
		os.Exit(1)
	}

	// Receive receiver meta (RTT + bandwidth probe)
	metaLine, _ := reader.ReadString('\n')
	metaLine = strings.TrimSpace(metaLine)

	var recvMeta ReceiverMeta
	json.Unmarshal([]byte(metaLine), &recvMeta)

	// RTT calculation
	rttMillis := time.Now().UnixMilli() - (recvMeta.Timestamp * 1000)
	rttSecs := float64(rttMillis) / 1000.0

	fmt.Println(greenf("RTT: %.2f ms", float64(rttMillis)))

	// Bandwidth estimation
	var bandwidth float64
	if strings.HasPrefix(recvMeta.Data, "ACK:") {
		var byteCount int64
		fmt.Sscanf(strings.TrimPrefix(recvMeta.Data, "ACK:"), "%d", &byteCount)

		if rttSecs > 0 {
			bandwidth = float64(byteCount) / rttSecs
		}
	}

	fmt.Println(greenf("Estimated Bandwidth: %.2f KB/s", bandwidth/1024))

	// -----------------------------
	// CHUNKING (metadata only)
	// -----------------------------
	jobs := make(chan models.Chunk, 100)

	totalChunks := internal.Chunker(
		file,
		models.Network{
			Bandwidth: bandwidth,
			RTT:       rttSecs,
		},
		4,              // workers
		1*1024*1024,    // buffer heuristic
		jobs,
	)

	fmt.Println(greenf("Total Chunks: %d", totalChunks))

	// -----------------------------
	// STREAMING (multi-connection)
	// -----------------------------
	cfg := models.StreamerConfig{
		Addr:        targetAddr,
		Workers:     4,        // parallel connections
		ChunkSize:   4 * 1024 * 1024,
		RetryBuffer: 50,
	}

	fmt.Println(greenf("Starting parallel streaming..."))

	err = internal.Streamer(file, jobs, cfg)
	if err != nil {
		fmt.Println(redf("Transfer failed: %v", err))
		os.Exit(1)
	}

	fmt.Println(greenf("Transfer complete"))
}
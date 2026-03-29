package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/shreyashsri79/openair-sender/internal"
	"github.com/shreyashsri79/openair-sender/internal/discovery"
	"github.com/shreyashsri79/openair-sender/internal/models"
)

type Meta struct {
	Name      string `json:"name"`
	Size      int64  `json:"size"`
	Timestamp int64
	Data      []byte //to calc rtt
}

type ReceiverMeta struct {
	Timestamp int64  `json:"timestamp"`
	Data      string `json:"data"`
}

func main() {
	if len(os.Args) < 2 {
		fmt.Println("Usage: go run sender.go <file_path>")
		os.Exit(1)
	}

	filePath := os.Args[1]

	// 1. Discover the phone via mDNS
	host, port, err := discovery.DiscoverAndroid()
	if err != nil {
		fmt.Printf("❌ Error: %v\n", err)
		os.Exit(1)
	}
	targetAddr := fmt.Sprintf("%s:%d", host, port)

	// 2. Prepare File Data
	file, err := os.Open(filePath)
	if err != nil {
		fmt.Printf("Failed to open file: %v\n", err)
		os.Exit(1)
	}
	defer file.Close()

	fileInfo, _ := file.Stat()
	// hasher := sha256.New()
	// io.Copy(hasher, file)
	// fileHash := hex.EncodeToString(hasher.Sum(nil))
	// file.Seek(0, 0) // Reset for actual transfer

	start := time.Now()

	meta := Meta{
		Timestamp: start.Unix(),
		Name:      filepath.Base(filePath),
		Size:      fileInfo.Size(),
		Data:      make([]byte, 1*1024*1024),
	}

	// 3. Connect and Transfer
	fmt.Printf(" Connecting to %s...\n", targetAddr)
	conn, err := net.DialTimeout("tcp", targetAddr, 5*time.Second)
	if err != nil {
		fmt.Printf("Connection failed: %v\n", err)
		os.Exit(1)
	}
	defer conn.Close()

	// Send JSON Header
	header, _ := json.Marshal(meta)
	fmt.Printf(" Sending Metadata: %s\n", string(header))
	conn.Write(append(header, '\n'))

	// Wait for Android's "ACCEPT" and meta response
	reader := bufio.NewReader(conn)
	response, err := reader.ReadString('\n')
	if err != nil {
		fmt.Printf(" Receiver error: %v\n", err)
		os.Exit(1)
	}

	// Check for ACCEPT and extract meta JSON if present

	var recvMeta ReceiverMeta

	if !strings.Contains(strings.ToUpper(response), "ACCEPT") {
		fmt.Printf(" Receiver rejected: %s\n", response)
		os.Exit(1)
	}

	// Get the next line which should be meta data
	metaLine, err := reader.ReadString('\n')
	if err != nil {
		fmt.Printf(" Error reading receiver meta: %v\n", err)
		os.Exit(1)
	}
	metaLine = strings.TrimSpace(metaLine)
	if err := json.Unmarshal([]byte(metaLine), &recvMeta); err != nil {
		fmt.Printf(" Failed to parse receiver meta: %v\n", err)
		os.Exit(1)
	}

	// RTT calculation: time now - receiver timestamp (assume epoch ms/sec consistency)
	rttMillis := (time.Now().UnixMilli()) - (recvMeta.Timestamp * 1000)
	rttSecs := float64(rttMillis) / 1000.0
	fmt.Printf(" Estimated RTT: %.2f ms\n", float64(rttMillis))

	// Bandwidth calculation if recvMeta.Data supports it (for example, number of bytes acknowledged)
	// Here, Data could be string bytes, e.g. "ACK:1048576"
	var bandwidth float64
	if strings.HasPrefix(recvMeta.Data, "ACK:") {
		countStr := strings.TrimPrefix(recvMeta.Data, "ACK:")
		var byteCount int64
		fmt.Sscanf(countStr, "%d", &byteCount)
		if rttSecs > 0 {
			bandwidth = float64(byteCount) / rttSecs // bytes/sec
			fmt.Printf(" Estimated Bandwidth: %.2f KB/s\n", bandwidth/1024)
		}
	}

	// Chunk the file
	worker := make(chan []byte, 1000)
	chunks := internal.Chunker(*file, models.Network{
		Bandwidth: bandwidth,
		RTT:       rttSecs,
	}, 10, 1024*1024, worker)

	hashChannel := make(chan []byte, 1000)
	streamChannel := make(chan []byte, 1000)

	for chunk := range worker {
		hashChannel <- chunk
		streamChannel <- chunk
		go func() {
			hash := internal.Hasher(hashChannel)
			streamChannel <- []byte(hash)
		}()
	}



	

	// Stream the file
	fmt.Printf(" Sending %s (%d bytes)...\n", meta.Name, meta.Size)
	sent, err := io.Copy(conn, file)
	if err != nil {
		fmt.Printf("\n Transfer interrupted: %v\n", err)
	}

	fmt.Printf("\n Success! Sent %d bytes.\n", sent)
}

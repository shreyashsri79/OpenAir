package sender

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

)

func greenf(format string, a ...any) string {
	return fmt.Sprintf(format, a...)
}

func redf(format string, a ...any) string {
	return fmt.Sprintf(format, a...)
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

func RunSender(filePath string, onLog func(string)) error {


	host, port, err := DiscoverAndroid()
	if err != nil {
		return fmt.Errorf("discovery error: %w", err)
	}
	targetAddr := fmt.Sprintf("%s:%d", host, port)

	file, err := os.Open(filePath)
	if err != nil {
		return fmt.Errorf("file open error: %w", err)
	}
	defer file.Close()

	fileInfo, _ := file.Stat()

	meta := Meta{
		Name:      filepath.Base(filePath),
		Size:      fileInfo.Size(),
		Timestamp: time.Now().Unix(),
	}

	conn, err := net.DialTimeout("tcp", targetAddr, 5*time.Second)
	if err != nil {
		return fmt.Errorf("connection failed: %w", err)
	}
	defer conn.Close()

	onLog(greenf("Connected to receiver"))

	header, _ := json.Marshal(meta)
	conn.Write(append(header, '\n'))

	reader := bufio.NewReader(conn)

	response, err := reader.ReadString('\n')
	if err != nil || !strings.Contains(strings.ToUpper(response), "ACCEPT") {
		return fmt.Errorf("receiver rejected: %v", response)
	}

	metaLine, _ := reader.ReadString('\n')
	metaLine = strings.TrimSpace(metaLine)

	var recvMeta ReceiverMeta
	json.Unmarshal([]byte(metaLine), &recvMeta)

	rttMillis := time.Now().UnixMilli() - (recvMeta.Timestamp * 1000)
	rttSecs := float64(rttMillis) / 1000.0

	var bandwidth float64
	if strings.HasPrefix(recvMeta.Data, "ACK:") {
		var byteCount int64
		fmt.Sscanf(strings.TrimPrefix(recvMeta.Data, "ACK:"), "%d", &byteCount)

		if rttSecs > 0 {
			bandwidth = float64(byteCount) / rttSecs
		}
	}

	jobs := make(chan Chunk, 100)

	totalChunks := Chunker(
		file,
		Network{
			Bandwidth: bandwidth,
			RTT:       rttSecs,
		},
		4,              // workers
		1*1024*1024,    // buffer heuristic
		jobs,
	)

	_ = totalChunks

	cfg := StreamerConfig{
		Addr:        targetAddr,
		Workers:     4,        // parallel connections
		ChunkSize:   4 * 1024 * 1024,
		RetryBuffer: 50,
	}

	onLog(greenf("Starting parallel streaming..."))

	err = Streamer(file, jobs, cfg)
	if err != nil {
		return fmt.Errorf("transfer failed: %w", err)
	}

	onLog(greenf("Transfer complete"))
	return nil
}
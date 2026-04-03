package main

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"time"

	"github.com/grandcat/zeroconf"
)

const (
	PORT        = 9000
	ServiceType = "_openair._tcp"
	Domain      = "local."

	ansiReset = "\x1b[0m"
	ansiGreen = "\x1b[32m"
	ansiRed   = "\x1b[31m"
)

// ---------- COLOR HELPERS ----------

func greenf(format string, a ...any) string {
	return ansiGreen + fmt.Sprintf(format, a...) + ansiReset
}

func redf(format string, a ...any) string {
	return ansiRed + fmt.Sprintf(format, a...) + ansiReset
}

// ---------- STRUCTS (must match sender) ----------

type Meta struct {
	Name      string `json:"name"`
	Size      int64  `json:"size"`
	Timestamp int64  `json:"timestamp"`
}

type ReceiverMeta struct {
	Timestamp int64  `json:"timestamp"`
	Data      string `json:"data"`
}

// ---------- GLOBAL STATE ----------

var (
	file        *os.File
	chunkSize   int64 = 4 * 1024 * 1024 // must match sender config
	initialized bool
)

// ---------- MAIN ----------

func main() {
	fmt.Println(greenf("Starting OpenAir Receiver..."))

	// Start mDNS
	mdns, err := zeroconf.Register(
		"OpenAir-Receiver",
		ServiceType,
		Domain,
		PORT,
		[]string{"app=OpenAir", "ver=1"},
		nil,
	)
	if err != nil {
		fmt.Println(redf("mDNS error: %v", err))
		return
	}
	defer mdns.Shutdown()

	fmt.Println(greenf("mDNS active on port %d", PORT))

	ln, err := net.Listen("tcp", fmt.Sprintf(":%d", PORT))
	if err != nil {
		fmt.Println(redf("Failed to listen: %v", err))
		return
	}
	defer ln.Close()

	fmt.Println(greenf("Listening on port %d", PORT))

	for {
		conn, err := ln.Accept()
		if err != nil {
			continue
		}

		go handleConnection(conn)
	}
}

// ---------- CONNECTION HANDLER ----------

func handleConnection(conn net.Conn) {
	defer conn.Close()

	reader := bufio.NewReader(conn)

	// Try reading first byte to detect if it's control or data
	peek, err := reader.Peek(1)
	if err != nil {
		return
	}

	// If it's '{' → JSON → control connection
	if peek[0] == '{' {
		handleControl(conn, reader)
		return
	}

	// Else → data connection
	handleData(conn, reader)
}

// ---------- CONTROL CONNECTION ----------

func handleControl(conn net.Conn, reader *bufio.Reader) {
	fmt.Println(greenf("Control connection established"))

	// Read metadata JSON
	line, err := reader.ReadString('\n')
	if err != nil {
		fmt.Println(redf("Failed to read metadata"))
		return
	}

	var meta Meta
	err = json.Unmarshal([]byte(strings.TrimSpace(line)), &meta)
	if err != nil {
		fmt.Println(redf("Invalid metadata"))
		return
	}

	fmt.Println(greenf("Incoming file: %s (%d bytes)", meta.Name, meta.Size))

	// Create output file
	file, err = os.Create("received_" + meta.Name)
	if err != nil {
		fmt.Println(redf("File creation failed: %v", err))
		return
	}

	// Pre-allocate file (important)
	file.Truncate(meta.Size)

	initialized = true

	// Send ACCEPT
	conn.Write([]byte("ACCEPT\n"))

	// Send RTT + bandwidth probe
	now := time.Now().Unix()
	dataSize := 1 * 1024 * 1024 // 1MB probe

	response := ReceiverMeta{
		Timestamp: now,
		Data:      fmt.Sprintf("ACK:%d", dataSize),
	}

	respJSON, _ := json.Marshal(response)
	conn.Write(append(respJSON, '\n'))

	fmt.Println(greenf("Handshake complete"))
}

// ---------- DATA CONNECTION ----------

func handleData(conn net.Conn, reader *bufio.Reader) {
	if !initialized {
		return
	}

	for {
		var chunkID int32
		var size int32

		// Read chunk ID
		if err := binary.Read(reader, binary.LittleEndian, &chunkID); err != nil {
			return
		}

		// Read chunk size
		if err := binary.Read(reader, binary.LittleEndian, &size); err != nil {
			return
		}

		buf := make([]byte, size)

		// Read chunk data
		_, err := io.ReadFull(reader, buf)
		if err != nil {
			return
		}

		// Compute offset (MVP assumption: fixed chunk size)
		offset := int64(chunkID) * chunkSize

		_, err = file.WriteAt(buf, offset)
		if err != nil {
			fmt.Println(redf("Write error"))
			return
		}
	}
}
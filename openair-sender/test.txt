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
	"sync"
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

// ---------- COLOR ----------

func greenf(format string, a ...any) string {
	return ansiGreen + fmt.Sprintf(format, a...) + ansiReset
}

func redf(format string, a ...any) string {
	return ansiRed + fmt.Sprintf(format, a...) + ansiReset
}

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

// ---------- GLOBAL STATE ----------

var (
	file        *os.File
	initialized bool
	mu          sync.Mutex // protects file writes (optional safety)
)

// ---------- MAIN ----------

func main() {
	fmt.Println(greenf("Starting OpenAir Receiver..."))

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

// ---------- ROUTER ----------

func handleConnection(conn net.Conn) {
	defer conn.Close()

	reader := bufio.NewReader(conn)

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
	fmt.Println(greenf("Control connection established"))

	line, err := reader.ReadString('\n')
	if err != nil {
		return
	}

	var meta Meta
	err = json.Unmarshal([]byte(strings.TrimSpace(line)), &meta)
	if err != nil {
		fmt.Println(redf("Invalid metadata"))
		return
	}

	fmt.Println(greenf("Incoming file: %s (%d bytes)", meta.Name, meta.Size))

	file, err = os.Create("received_" + meta.Name)
	if err != nil {
		fmt.Println(redf("File creation failed: %v", err))
		return
	}

	// pre-allocate
	file.Truncate(meta.Size)

	initialized = true

	// ACCEPT
	conn.Write([]byte("ACCEPT\n"))

	// FIXED RTT (milliseconds)
	now := time.Now().UnixMilli()

	resp := ReceiverMeta{
		Timestamp: now,
		Data:      fmt.Sprintf("ACK:%d", 1024*1024),
	}

	js, _ := json.Marshal(resp)
	conn.Write(append(js, '\n'))

	fmt.Println(greenf("Handshake complete"))
}

// ---------- DATA ----------

func handleData(conn net.Conn, reader *bufio.Reader) {
	if !initialized {
		return
	}

	for {
		var offset int64
		var size int32

		// read offset
		if err := binary.Read(reader, binary.LittleEndian, &offset); err != nil {
			return
		}

		// read size
		if err := binary.Read(reader, binary.LittleEndian, &size); err != nil {
			return
		}

		buf := make([]byte, size)

		_, err := io.ReadFull(reader, buf)
		if err != nil {
			return
		}

		// safe write
		mu.Lock()
		_, err = file.WriteAt(buf, offset)
		mu.Unlock()

		if err != nil {
			fmt.Println(redf("Write error: %v", err))
			return
		}
	}
}
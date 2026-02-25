package main

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/grandcat/zeroconf"
)

type FileMeta struct {
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

func discoverAndroid() (string, int, error) {
	fmt.Println("🔍 Scanning for OpenAir Android device...")
	resolver, err := zeroconf.NewResolver(nil)
	if err != nil {
		return "", 0, err
	}

	entries := make(chan *zeroconf.ServiceEntry)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	go func() {
		err = resolver.Browse(ctx, "_openair._tcp", "local.", entries)
		if err != nil {
			fmt.Println("Browse failed:", err)
		}
	}()

	select {
	case entry := <-entries:
		// Prefer IPv4
		addr := entry.AddrIPv4[0].String()
		fmt.Printf("✅ Found: %s at %s:%d\n", entry.Instance, addr, entry.Port)
		return addr, entry.Port, nil
	case <-ctx.Done():
		return "", 0, fmt.Errorf("discovery timed out")
	}
}

func main() {
	if len(os.Args) < 2 {
		fmt.Println("Usage: go run sender.go <file_path>")
		os.Exit(1)
	}

	filePath := os.Args[1]

	// 1. Discover the phone via mDNS
	host, port, err := discoverAndroid()
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
	hasher := sha256.New()
	io.Copy(hasher, file)
	fileHash := hex.EncodeToString(hasher.Sum(nil))
	file.Seek(0, 0) // Reset for actual transfer

	meta := FileMeta{
		Name:   filepath.Base(filePath),
		Size:   fileInfo.Size(),
		SHA256: fileHash,
	}

	// 3. Connect and Transfer
	fmt.Printf("🚀 Connecting to %s...\n", targetAddr)
	conn, err := net.DialTimeout("tcp", targetAddr, 5*time.Second)
	if err != nil {
		fmt.Printf("Connection failed: %v\n", err)
		os.Exit(1)
	}
	defer conn.Close()

	// Send JSON Header
	header, _ := json.Marshal(meta)
	fmt.Printf("📤 Sending Metadata: %s\n", string(header))
	conn.Write(append(header, '\n'))

	// Wait for Android's "ACCEPT"
	reader := bufio.NewReader(conn)
	response, err := reader.ReadString('\n')
	if err != nil || !strings.Contains(strings.ToUpper(response), "ACCEPT") {
		fmt.Printf("❌ Receiver rejected or error: %v (Response: %s)\n", err, response)
		os.Exit(1)
	}

	// Stream the file
	fmt.Printf("📦 Sending %s (%d bytes)...\n", meta.Name, meta.Size)
	sent, err := io.Copy(conn, file)
	if err != nil {
		fmt.Printf("\n❌ Transfer interrupted: %v\n", err)
	}

	fmt.Printf("\n✅ Success! Sent %d bytes.\n", sent)
}

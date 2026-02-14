package main

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
)

const (
	ReceiverAddr = "localhost:8080"
)

type FileMeta struct {
	Name   string `json:"name"`
	Size   int64  `json:"sizw"`
	SHA256 string `json:"sha26"`
}

func main() {
	if len(os.Args) < 2 {
		fmt.Println("Usage: go run sender.go <file.txt>")
		os.Exit(1)
	}

	filePath := os.Args[1]

	// 1) Open and validate the file
	file, err := os.Open(filePath)
	if err != nil {
		fmt.Println("Failed to open file:", err)
		os.Exit(1)
	}
	defer file.Close()

	fileInfo, err := file.Stat()
	if err != nil {
		fmt.Println("Failed to stat file:", err)
		os.Exit(1)
	}

	// 2) Calculate SHA-256 hash
	fmt.Println("Calculating file hash...")
	hasher := sha256.New()
	if _, err := io.Copy(hasher, file); err != nil {
		fmt.Println("Failed to hash file:", err)
		os.Exit(1)
	}
	fileHash := hex.EncodeToString(hasher.Sum(nil))

	// Reset file pointer to beginning
	if _, err := file.Seek(0, 0); err != nil {
		fmt.Println("Failed to reset file pointer:", err)
		os.Exit(1)
	}

	// 3) Prepare metadata
	meta := FileMeta{
		Name:   filepath.Base(filePath),
		Size:   fileInfo.Size(),
		SHA256: fileHash,
	}

	fmt.Printf("\nPreparing to send:\n")
	fmt.Printf("  File: %s\n", meta.Name)
	fmt.Printf("  Size: %s\n", formatBytes(meta.Size))
	fmt.Printf("  Hash: %s\n", meta.SHA256)
	fmt.Printf("  To:   %s\n", ReceiverAddr)

	// 4) Connect to receiver
	fmt.Println("\nConnecting to receiver...")
	conn, err := net.Dial("tcp", ReceiverAddr)
	if err != nil {
		fmt.Println("Failed to connect:", err)
		os.Exit(1)
	}
	defer conn.Close()

	fmt.Println("Connected!")

	// 5) Send JSON header
	headerJSON, err := json.Marshal(meta)
	if err != nil {
		fmt.Println("Failed to marshal metadata:", err)
		os.Exit(1)
	}

	fmt.Printf("\nSending JSON header: %s\n", string(headerJSON))

	headerWithNewline := append(headerJSON, '\n')
	if _, err := conn.Write(headerWithNewline); err != nil {
		fmt.Println("Failed to send header:", err)
		os.Exit(1)
	}

	// 6) Wait for ACCEPT or REJECT
	reader := bufio.NewReader(conn)
	response, err := reader.ReadString('\n')
	if err != nil {
		fmt.Println("Failed to read response:", err)
		os.Exit(1)
	}

	response = strings.TrimSpace(response)
	if response != "ACCEPT" {
		fmt.Println("Receiver rejected the file:", response)
		os.Exit(1)
	}

	fmt.Println("Receiver accepted. Sending file...")

	// 7) Send file data
	sent, err := io.Copy(conn, file)
	if err != nil {
		fmt.Println("Failed to send file:", err)
		os.Exit(1)
	}

	if sent != meta.Size {
		fmt.Printf("Warning: sent %d bytes, expected %d\n", sent, meta.Size)
	}

	fmt.Println("\nTransfer complete!")
	fmt.Printf("Sent %s successfully.\n", file)
}


func formatBytes(n int64) string {
	const (
		KB = 1024
		MB = 1024 * KB
		GB = 1024 * MB
	)

	switch {
	case n >= GB:
		return fmt.Sprintf("%.2f GB", float64(n)/float64(GB))
	case n >= MB:
		return fmt.Sprintf("%.2f MB", float64(n)/float64(MB))
	case n >= KB:
		return fmt.Sprintf("%.2f KB", float64(n)/float64(KB))
	default:
		return fmt.Sprintf("%d B", n)
	}
}
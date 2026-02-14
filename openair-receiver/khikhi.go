package main

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	ListenPort     = 8989
	MaxFileSize    = int64(2 * 1024 * 1024 * 1024) // 2 GB
	ReadTimeoutSec = 30
)

type FileMeta struct {
	Filename string `json:"filename"`
	Size     int64  `json:"size"`
	SHA256   string `json:"sha256"`
}

func main() {
	addr := ":" + strconv.Itoa(ListenPort)

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		fmt.Println("Failed to listen:", err)
		return
	}
	defer ln.Close()

	fmt.Printf("OpenAir Receiver listening on %s\n", addr)

	for {
		conn, err := ln.Accept()
		if err != nil {
			fmt.Println("Accept error:", err)
			continue
		}

		// Handle one at a time for clean terminal UX (exhibition-friendly).
		// You can change this to `go handleConn(conn)` later.
		handleConn(conn)
	}
}

func handleConn(conn net.Conn) {
	defer conn.Close()

	remote := conn.RemoteAddr().String()
	fmt.Printf("\nIncoming connection from %s\n", remote)

	conn.SetReadDeadline(time.Now().Add(ReadTimeoutSec * time.Second))

	reader := bufio.NewReader(conn)

	// 1) Read JSON header line
	headerLine, err := reader.ReadString('\n')
	if err != nil {
		fmt.Println("Failed to read header:", err)
		return
	}

	headerLine = strings.TrimSpace(headerLine)

	var meta FileMeta
	if err := json.Unmarshal([]byte(headerLine), &meta); err != nil {
		fmt.Println("Invalid JSON header:", err)
		return
	}

	// 2) Validate metadata
	if err := validateMeta(&meta); err != nil {
		fmt.Println("Invalid metadata:", err)
		return
	}

	// Sanitize filename
	meta.Filename = sanitizeFilename(meta.Filename)

	fmt.Printf("Incoming file request:\n")
	fmt.Printf("  From: %s\n", remote)
	fmt.Printf("  File: %s\n", meta.Filename)
	fmt.Printf("  Size: %s\n", formatBytes(meta.Size))
	fmt.Printf("Accept? (y/n): ")

	// 3) Ask accept/reject locally
	in := bufio.NewReader(os.Stdin)
	answer, _ := in.ReadString('\n')
	answer = strings.TrimSpace(strings.ToLower(answer))

	if answer != "y" && answer != "yes" {
		_, _ = conn.Write([]byte("REJECT\n"))
		fmt.Println("Rejected.")
		return
	}

	_, _ = conn.Write([]byte("ACCEPT\n"))
	fmt.Println("Accepted. Receiving...")

	// Remove read deadline for large file transfer
	_ = conn.SetReadDeadline(time.Time{})

	// 4) Prepare output path
	saveDir, err := getDownloadsDir()
	if err != nil {
		fmt.Println("Failed to resolve Downloads directory:", err)
		return
	}

	saveDir = filepath.Join(saveDir, "OpenAir")
	if err := os.MkdirAll(saveDir, 0755); err != nil {
		fmt.Println("Failed to create save directory:", err)
		return
	}

	finalPath := uniquePath(filepath.Join(saveDir, meta.Filename))
	tempPath := finalPath + ".part"

	out, err := os.Create(tempPath)
	if err != nil {
		fmt.Println("Failed to create output file:", err)
		return
	}
	defer out.Close()

	// 5) Receive exactly meta.Size bytes
	hasher := sha256.New()

	limited := io.LimitReader(reader, meta.Size)
	written, err := io.Copy(io.MultiWriter(out, hasher), limited)
	if err != nil {
		fmt.Println("Transfer failed:", err)
		_ = os.Remove(tempPath)
		return
	}

	// Confirm exact size
	if written != meta.Size {
		fmt.Printf("Transfer incomplete: got %d bytes, expected %d\n", written, meta.Size)
		_ = os.Remove(tempPath)
		return
	}

	// 6) Verify hash
	gotHash := hex.EncodeToString(hasher.Sum(nil))
	if !strings.EqualFold(gotHash, meta.SHA256) {
		fmt.Println("SHA-256 mismatch!")
		fmt.Println("Expected:", meta.SHA256)
		fmt.Println("Got     :", gotHash)
		_ = os.Remove(tempPath)
		return
	}

	// 7) Finalize file
	if err := os.Rename(tempPath, finalPath); err != nil {
		fmt.Println("Failed to finalize file:", err)
		_ = os.Remove(tempPath)
		return
	}

	fmt.Println("Transfer complete!")
	fmt.Println("Saved to:", finalPath)
}

func validateMeta(meta *FileMeta) error {
	if meta.Filename == "" {
		return errors.New("filename is empty")
	}
	if meta.Size <= 0 {
		return errors.New("invalid file size")
	}
	if meta.Size > MaxFileSize {
		return errors.New("file too large (blocked by receiver limit)")
	}
	if len(meta.SHA256) != 64 {
		return errors.New("sha256 must be 64 hex chars")
	}
	// Basic hex check
	_, err := hex.DecodeString(meta.SHA256)
	if err != nil {
		return errors.New("sha256 is not valid hex")
	}
	return nil
}

func sanitizeFilename(name string) string {
	name = strings.TrimSpace(name)
	name = strings.ReplaceAll(name, "\x00", "")

	// Remove path separators
	name = strings.ReplaceAll(name, "/", "_")
	name = strings.ReplaceAll(name, "\\", "_")

	// Prevent traversal-like patterns
	for strings.Contains(name, "..") {
		name = strings.ReplaceAll(name, "..", "_")
	}

	// Avoid empty name after sanitization
	if name == "" {
		return "file"
	}

	return name
}

func uniquePath(path string) string {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return path
	}

	dir := filepath.Dir(path)
	base := filepath.Base(path)
	ext := filepath.Ext(base)
	name := strings.TrimSuffix(base, ext)

	for i := 1; i < 10000; i++ {
		candidate := filepath.Join(dir, fmt.Sprintf("%s (%d)%s", name, i, ext))
		if _, err := os.Stat(candidate); os.IsNotExist(err) {
			return candidate
		}
	}

	// Worst case fallback
	return filepath.Join(dir, fmt.Sprintf("%s (%d)%s", name, time.Now().Unix(), ext))
}

func getDownloadsDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Downloads"), nil
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


package internal

import (
	"fmt"
	"os"

	"github.com/shreyashsri79/openair-sender/internal/models"
)

func clamp(val, min, max int64) int64 {
	if val < min {
		return min
	}
	if val > max {
		return max
	}
	return val
}

func min(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}

// fallbackChunker → used when network metrics are broken
func fallbackChunker(file *os.File, jobs chan<- models.Chunk) int {
	fmt.Println("[DEBUG] Using fallback chunker")

	fileStat, err := file.Stat()
	if err != nil {
		panic(err)
	}

	fileSize := fileStat.Size()
	var offset int64 = 0
	chunkSize := int64(1 * 1024 * 1024) // 1MB default

	count := 0

	for offset < fileSize {
		size := min(fileSize-offset, chunkSize)

		jobs <- models.Chunk{
			ID:     count,
			Offset: offset,
			Size:   size,
			Retry:  0,
		}

		offset += size
		count++
	}

	close(jobs)
	return count
}

func Chunker(
	file *os.File,
	network models.Network, // Bandwidth (bytes/sec), RTT (seconds)
	workerNum float64,
	bufferSize float64, // not used directly anymore (kept for compatibility)
	jobs chan<- models.Chunk,
) int {

	fmt.Println("[DEBUG] Chunker invoked")

	fileStat, err := file.Stat()
	if err != nil {
		panic(err)
	}

	fileSize := fileStat.Size()

	// 🚨 Validate network inputs
	if network.Bandwidth <= 0 || network.RTT <= 0 {
		fmt.Printf("[DEBUG] Invalid network metrics → Bandwidth=%.2f RTT=%.2f\n",
			network.Bandwidth, network.RTT)
		return fallbackChunker(file, jobs)
	}

	// ✅ Bandwidth Delay Product (bytes)
	bdp := network.Bandwidth * network.RTT
	fmt.Printf("[DEBUG] BDP = %.2f bytes\n", bdp)

	// ✅ Distribute across workers
	chunkSize := int64(bdp / workerNum)

	// 🚨 Safety fallback if too small
	if chunkSize <= 0 {
		chunkSize = 1 * 1024 * 1024 // 1MB
	}

	// ✅ Clamp (important)
	chunkSize = clamp(
		chunkSize,
		256*1024,      // 256KB min
		4*1024*1024,   // 4MB max
	)

	fmt.Printf("[DEBUG] Final chunkSize = %d bytes\n", chunkSize)

	// 🔁 Generate chunk jobs
	var offset int64 = 0
	chunkCounter := 0

	for offset < fileSize {
		size := min(fileSize-offset, chunkSize)

		jobs <- models.Chunk{
			ID:     chunkCounter,
			Offset: offset,
			Size:   size,
			Retry:  0,
		}

		offset += size
		chunkCounter++
	}

	close(jobs)

	fmt.Printf("[DEBUG] Total chunks = %d\n", chunkCounter)

	return chunkCounter
}
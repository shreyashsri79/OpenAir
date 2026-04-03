package internal

import (
	"math"
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

// min helper for int64
func min(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}


func Chunker(
	file *os.File,
	network models.Network,
	workerNum float64,
	bufferSize float64,
	jobs chan<- models.Chunk,
) int {

	// Bandwidth-Delay Product (ensure units: bytes/sec * seconds)
	networkBased := network.Bandwidth * network.RTT

	fileStat, err := file.Stat()
	if err != nil {
		panic(err)
	}

	fileSize := fileStat.Size()

	// parallel heuristic
	parallelBased := math.Ceil(float64(fileSize) / (workerNum * bufferSize))

	// choose larger of the two
	chunkSize := int64(math.Max(networkBased, parallelBased))

	// clamp to sane limits (32KB → 4MB)
	chunkSize = clamp(
		chunkSize,
		32*1024,
		4*1024*1024,
	)

	var offset int64 = 0
	chunkCounter := 0

	for offset < fileSize {
		remaining := fileSize - offset
		currentChunkSize := min(remaining, chunkSize)

		jobs <- models.Chunk{
			ID:     chunkCounter,
			Offset: offset,
			Size:   currentChunkSize,
			Retry:  0,
		}

		offset += currentChunkSize
		chunkCounter++
	}

	close(jobs)
	return chunkCounter
}
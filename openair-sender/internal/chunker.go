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

func Chunker(file os.File, network models.Network, workerNum float64, bufferSize float64, worker chan<- []byte) int {

	networkBased := network.Bandwidth * network.RTT

	fileStat, err := file.Stat()
	if err != nil {
		panic(err)
	}
	parallelBased := math.Ceil(float64(fileStat.Size()) / (workerNum * bufferSize))

	chunkSize := int64(math.Max(
		networkBased,
		parallelBased,
	))

	chunkSize = clamp(
		chunkSize,
		32*1024,
		4*1024*1024,
	)

	fileSize := fileStat.Size()
	var offset int64 = 0

	chunkCounter := 0
	for offset < fileSize {
		remaining := fileSize - offset
		currentChunkSize := min(remaining, chunkSize)

		chunk := make([]byte, currentChunkSize)
		n, err := file.ReadAt(chunk, offset)
		if err != nil && err.Error() != "EOF" {
			panic(err)
		}
		worker <- chunk[:n]
		offset += int64(n)
		chunkCounter++
	}
	close(worker)

	return chunkCounter
}

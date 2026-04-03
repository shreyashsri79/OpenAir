package internal

import (
	"encoding/binary"
	"errors"
	"io"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/shreyashsri79/openair-sender/internal/models"
)

const maxRetry = 3


func Streamer(file *os.File, jobs <-chan models.Chunk, cfg models.StreamerConfig) error {
	retry := make(chan models.Chunk, cfg.RetryBuffer)
	errCh := make(chan error, 1)

	var wg sync.WaitGroup
	var bytesSent int64

	
	for i := int16(0); i < cfg.Workers; i++ {
		wg.Add(1)
		go worker(file, jobs, retry, errCh, &bytesSent, cfg, &wg)
	}

	// wait async
	go func() {
		wg.Wait()
		close(errCh)
	}()

	// monitor bandwidth (optional)
	go monitor(&bytesSent)

	// error handling
	if err, ok := <-errCh; ok {
		return err
	}

	return nil
}

func worker(
	file *os.File,
	jobs <-chan models.Chunk,
	retry chan models.Chunk,
	errCh chan<- error,
	bytesSent *int64,
	cfg models.StreamerConfig,
	wg *sync.WaitGroup,
) {
	defer wg.Done()

	conn, err := net.Dial("tcp", cfg.Addr)
	if err != nil {
		select {
		case errCh <- err:
		default:
		}
		return
	}
	defer conn.Close()

	buf := make([]byte, cfg.ChunkSize)

	for {
		var chunk models.Chunk
		var ok bool

		
		select {
		case chunk = <-retry:
			ok = true
		default:
			chunk, ok = <-jobs
		}

		if !ok {
			return
		}

		err := sendChunk(conn, file, buf, chunk, bytesSent)

		if err != nil {
			chunk.Retry++

			if chunk.Retry > maxRetry {
				select {
				case errCh <- errors.New("chunk failed permanently"):
				default:
				}
				return
			}

			retry <- chunk
		}
	}
}
func sendChunk(
	conn net.Conn,
	file *os.File,
	buf []byte,
	chunk models.Chunk,
	bytesSent *int64,
) error {

	n, err := file.ReadAt(buf[:chunk.Size], chunk.Offset)
	if err != nil && err != io.EOF {
		return err
	}

	// header: 
	//[chunkID]
	if err := binary.Write(conn, binary.LittleEndian, int32(chunk.ID)); err != nil {
		return err
	}
	//[size]
	if err := binary.Write(conn, binary.LittleEndian, int32(n)); err != nil {
		return err
	}

	// data
	written, err := conn.Write(buf[:n])
	if err != nil {
		return err
	}

	atomic.AddInt64(bytesSent, int64(written))

	return nil
}

func monitor(bytesSent *int64) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	var last int64

	for range ticker.C {
		curr := atomic.LoadInt64(bytesSent)
		delta := curr - last
		last = curr

		mbps := (float64(delta) * 8) / 2_000_000
		println("Speed:", mbps, "Mbps")
	}
}

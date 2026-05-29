package sender

import (
	"encoding/binary"
	"errors"
	"io"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"time"
	"fmt"

)

const maxRetry = 3

func Streamer(file *os.File, jobs <-chan Chunk, cfg StreamerConfig) error {
	fmt.Printf("[DEBUG] Streamer called with file=%v, addr=%s, workers=%d, chunkSize=%d, retryBuffer=%d\n", file.Name(), cfg.Addr, cfg.Workers, cfg.ChunkSize, cfg.RetryBuffer)
	retry := make(chan Chunk, cfg.RetryBuffer)
	errCh := make(chan error, 1)

	var wg sync.WaitGroup
	var bytesSent int64

	for i := int16(0); i < cfg.Workers; i++ {
		wg.Add(1)
		fmt.Printf("[DEBUG] Launching worker #%d\n", i)
		go worker(file, jobs, retry, errCh, &bytesSent, cfg, &wg)
	}

	// wait async
	go func() {
		wg.Wait()
		fmt.Println("[DEBUG] All workers done, closing errCh")
		close(errCh)
	}()

	// monitor bandwidth (optional)
	go monitor(&bytesSent)

	// error handling
	if err, ok := <-errCh; ok {
		fmt.Printf("[DEBUG] Error received on errCh: %v\n", err)
		return err
	}
	fmt.Println("[DEBUG] Streamer finished without error")
	return nil
}

func worker(
	file *os.File,
	jobs <-chan Chunk,
	retry chan Chunk,
	errCh chan<- error,
	bytesSent *int64,
	cfg StreamerConfig,
	wg *sync.WaitGroup,
) {
	defer wg.Done()

	fmt.Printf("[DEBUG] worker started for file=%s addr=%s\n", file.Name(), cfg.Addr)
	conn, err := net.Dial("tcp", cfg.Addr)
	if err != nil {
		fmt.Printf("[DEBUG] worker failed to dial TCP: %v\n", err)
		select {
		case errCh <- err:
		default:
		}
		return
	}
	defer func() {
		fmt.Println("[DEBUG] worker closing connection")
		conn.Close()
	}()

	buf := make([]byte, cfg.ChunkSize)

	for {
		var chunk Chunk
		var ok bool

		select {
		case chunk = <-retry:
			ok = true
			fmt.Printf("[DEBUG] Worker retrying chunk: %+v\n", chunk)
		default:
			chunk, ok = <-jobs
			if ok {
				fmt.Printf("[DEBUG] Worker fetched chunk from jobs: %+v\n", chunk)
			}
		}

		if !ok {
			fmt.Println("[DEBUG] Worker: no more jobs, exiting loop")
			return
		}

		fmt.Printf("[DEBUG] Worker sending chunk ID=%d Offset=%d Size=%d Retry=%d\n", chunk.ID, chunk.Offset, chunk.Size, chunk.Retry)
		err := sendChunk(conn, file, buf, chunk, bytesSent)

		if err != nil {
			fmt.Printf("[DEBUG] sendChunk error for chunk ID=%d: %v\n", chunk.ID, err)
			chunk.Retry++
			fmt.Printf("[DEBUG] Retrying chunk ID=%d, attempt #%d\n", chunk.ID, chunk.Retry)

			if chunk.Retry > maxRetry {
				fmt.Printf("[DEBUG] chunk ID=%d failed permanently\n", chunk.ID)
				select {
				case errCh <- errors.New("chunk failed permanently"):
				default:
				}
				return
			}

			retry <- chunk
		} else {
			fmt.Printf("[DEBUG] Chunk ID=%d sent successfully\n", chunk.ID)
		}
	}
}

func sendChunk(
	conn net.Conn,
	file *os.File,
	buf []byte,
	chunk Chunk,
	bytesSent *int64,
) error {
	fmt.Printf("[DEBUG] sendChunk called with chunk=%+v\n", chunk)
	n, err := file.ReadAt(buf[:chunk.Size], chunk.Offset)
	if err != nil && err != io.EOF {
		fmt.Printf("[DEBUG] Error reading from file at offset=%d size=%d: %v\n", chunk.Offset, chunk.Size, err)
		return err
	}

	fmt.Printf("[DEBUG] Read %d bytes from file at offset %d for chunk ID=%d\n", n, chunk.Offset, chunk.ID)

	// header: 
	//[chunkOffset]
	if err := binary.Write(conn, binary.LittleEndian, int64(chunk.Offset)); err != nil {
		fmt.Printf("[DEBUG] Error writing chunkID %d to conn: %v\n", chunk.ID, err)
		return err
	}
	fmt.Printf("[DEBUG] Sent header chunkID=%d\n", chunk.ID)
	//[size]
	if err := binary.Write(conn, binary.LittleEndian, int32(n)); err != nil {
		fmt.Printf("[DEBUG] Error writing size %d to conn: %v\n", n, err)
		return err
	}
	fmt.Printf("[DEBUG] Sent header size=%d for chunk ID=%d\n", n, chunk.ID)

	// data
	written, err := conn.Write(buf[:n])
	if err != nil {
		fmt.Printf("[DEBUG] Error writing buffer to conn for chunk ID=%d: %v\n", chunk.ID, err)
		return err
	}

	fmt.Printf("[DEBUG] Sent %d bytes data for chunk ID=%d\n", written, chunk.ID)
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
		fmt.Printf("[DEBUG] Bandwidth Monitor: Speed: %.3f Mbps | Bytes sent in last 2s: %d\n", mbps, delta)
	}
}

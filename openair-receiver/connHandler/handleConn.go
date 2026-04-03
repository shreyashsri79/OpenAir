package connhandler

import (
	"encoding/binary"
	"io"
	"net"
	"os"
)

const CHUNK_SIZE = 4 * 1024 * 1024 // must match sender (MVP assumption)

// InitFile creates shared file
func InitFile(name string) (*os.File, error) {
	return os.Create(name)
}

func HandleConn(conn net.Conn, file *os.File) {
	defer conn.Close()

	for {
		var chunkID int32
		var size int32

		// Read chunk ID
		if err := binary.Read(conn, binary.LittleEndian, &chunkID); err != nil {
			return
		}

		// Read chunk size
		if err := binary.Read(conn, binary.LittleEndian, &size); err != nil {
			return
		}

		// Read chunk data
		buf := make([]byte, size)
		if _, err := io.ReadFull(conn, buf); err != nil {
			return
		}

		// Compute offset (MVP assumption: fixed chunk size)
		offset := int64(chunkID) * CHUNK_SIZE

		// Write safely
		file.WriteAt(buf, offset)
	}
}
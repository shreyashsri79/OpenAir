package internal

import (
	"crypto/sha256"
	"encoding/hex"
)

func Hasher(worker <-chan []byte) string {
	hasher := sha256.New()
	for chunk := range worker {
		// Debug print for each received chunk
		println("[DEBUG] Hasher received chunk of size:", len(chunk))
		hasher.Write(chunk)
	}
	return hex.EncodeToString(hasher.Sum(nil))
}


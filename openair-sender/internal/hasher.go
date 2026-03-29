package internal

import (
	"crypto/sha256"
	"encoding/hex"
)

func Hasher(worker <-chan []byte) string {
	hasher := sha256.New()
	for chunk := range worker {
		hasher.Write(chunk)
	}
	return hex.EncodeToString(hasher.Sum(nil))
}


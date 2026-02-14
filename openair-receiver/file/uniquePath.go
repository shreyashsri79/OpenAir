package file

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func UniquePath(path string) string {
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

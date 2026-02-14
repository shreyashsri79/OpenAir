package file

import (
	"os"
	"path/filepath"
)

func GetOpenAirDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}

	dir := filepath.Join(home, "OpenAir")

	// Create if it doesn't exist
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", err
	}

	return dir, nil
}

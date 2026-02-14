package file

import (
	"encoding/hex"
	"errors"

	"strings"

	"github.com/shreyashsri79/openair-receiver/constants"
	errorhandler "github.com/shreyashsri79/openair-receiver/errorHandler"
	"github.com/shreyashsri79/openair-receiver/models"
)

func ValidateAndSanitizeFile(meta *models.FileMetaData) models.FileMetaData {

	if err := validateFile(meta); err != nil {
		errorhandler.FatalRed("file cant be validated", err)
	}

	meta.Name = sanitizeFilename(meta.Name)
	return *meta
}

func validateFile(meta *models.FileMetaData) error {
	if meta.Name == "" {
		return errors.New("filename is empty")
	}
	if meta.Size <= 0 {
		return errors.New("invalid file size")
	}
	if meta.Size > constants.MaxFileSize {
		return errors.New("file too large (blocked by receiver limit)")
	}
	if len(meta.SHA256) != 64 {
		return errors.New("sha256 must be 64 hex chars")
	}
	// Basic hex check
	_, err := hex.DecodeString(meta.SHA256)
	if err != nil {
		return errors.New("sha256 is not valid hex")
	}
	return nil
}

func sanitizeFilename(name string) string {
	name = strings.TrimSpace(name)
	name = strings.ReplaceAll(name, "\x00", "")

	// Remove path separators
	name = strings.ReplaceAll(name, "/", "_")
	name = strings.ReplaceAll(name, "\\", "_")

	// Prevent traversal-like patterns
	for strings.Contains(name, "..") {
		name = strings.ReplaceAll(name, "..", "_")
	}

	// Avoid empty name after sanitization
	if name == "" {
		return "file"
	}

	return name
}

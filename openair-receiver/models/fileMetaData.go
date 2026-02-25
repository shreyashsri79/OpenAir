package models

type FileMetaData struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

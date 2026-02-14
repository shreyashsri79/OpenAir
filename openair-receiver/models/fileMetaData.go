package models

type FileMetaData struct {
	Name string `json:"name"`
	Size int64  `json:"sizw"`
	SHA256 string `json:"sha26"`
}

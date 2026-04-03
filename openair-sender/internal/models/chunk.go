package models

type Chunk struct {
	ID     int
	Offset int64
	Size   int64
	Retry  int
}
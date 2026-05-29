package sender

type StreamerConfig struct {
	Addr        string
	Workers     int16
	ChunkSize   int64
	RetryBuffer int16
}
//go:build !linux

package bench

// cpuSeconds is a no-op off Linux. The spike targets Linux; when this harness
// is eventually run on Windows to answer K1 for real, implement this with
// GetProcessTimes.
func cpuSeconds() float64 { return 0 }

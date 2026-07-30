//go:build !linux && !windows

package bench

// cpuSeconds is unimplemented on this platform. Results will report zero CPU,
// which is visibly wrong rather than plausibly wrong.
func cpuSeconds() float64 { return 0 }

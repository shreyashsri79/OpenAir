//go:build linux

package bench

import "syscall"

// cpuSeconds returns user+system CPU consumed by this process so far.
// Wall-clock throughput alone can't answer the Windows question; CPU cost per
// byte can.
func cpuSeconds() float64 {
	var ru syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &ru); err != nil {
		return 0
	}
	tv := func(t syscall.Timeval) float64 {
		return float64(t.Sec) + float64(t.Usec)/1e6
	}
	return tv(ru.Utime) + tv(ru.Stime)
}

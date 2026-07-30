//go:build windows

package bench

import "golang.org/x/sys/windows"

// cpuSeconds returns user+kernel CPU consumed by this process.
//
// This exists because the first Windows baseline run reported zero for every
// CPU figure: the non-Linux fallback was a stub, while the runbook told the
// operator the CPU column was the one that mattered. Throughput alone cannot
// distinguish a transport that is link-bound from one that is CPU-bound.
func cpuSeconds() float64 {
	var creation, exit, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(windows.CurrentProcess(), &creation, &exit, &kernel, &user); err != nil {
		return 0
	}
	// FILETIME counts 100-nanosecond intervals.
	sec := func(ft windows.Filetime) float64 {
		return float64(uint64(ft.HighDateTime)<<32|uint64(ft.LowDateTime)) / 1e7
	}
	return sec(kernel) + sec(user)
}

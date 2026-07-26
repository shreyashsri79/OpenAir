package bench

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
)

// Result is one row of the benchmark matrix. Emitted as JSON Lines so
// netem/matrix.sh can concatenate runs and jq can aggregate them.
type Result struct {
	Transport  string `json:"transport"` // "tcp" | "quic"
	Streams    int    `json:"streams"`
	ChunkBytes int64  `json:"chunk_bytes"`
	TotalBytes int64  `json:"total_bytes"`

	SetupSec    float64 `json:"setup_sec"`    // dial + handshake + all channels up
	TransferSec float64 `json:"transfer_sec"` // first byte sent -> receiver acked completion
	Mbps        float64 `json:"mbps"`         // goodput, application bytes

	// CPUSecPerGiB is the number that predicts off-Linux behaviour. QUIC is
	// userspace: it pays per-packet crypto and syscall costs the kernel pays
	// for TCP. A QUIC run that matches TCP throughput while burning several
	// times the CPU has not passed -- it has just not hit the wall yet.
	SenderCPUSec float64 `json:"sender_cpu_sec"`
	CPUSecPerGiB float64 `json:"cpu_sec_per_gib"`

	// Probes is empty unless -probe was set. Idle and busy entries appear in
	// pairs so the inflation caused by bulk is readable without another run.
	Probes []ProbeStats `json:"probes,omitempty"`

	GSO     string `json:"gso"`               // "on" | "off" | "n/a"
	Profile string `json:"profile,omitempty"` // netem profile label
	Label   string `json:"label,omitempty"`
	Error   string `json:"error,omitempty"`
}

func (r *Result) finalize(cpuSec float64) {
	r.SenderCPUSec = cpuSec
	if r.TransferSec > 0 {
		r.Mbps = float64(r.TotalBytes) * 8 / r.TransferSec / 1e6
	}
	if gib := float64(r.TotalBytes) / (1 << 30); gib > 0 {
		r.CPUSecPerGiB = cpuSec / gib
	}
}

// Emit writes the result as one JSON line to stdout, plus a human summary to
// stderr so an interactive run is readable without piping through jq.
func (r *Result) Emit() {
	enc := json.NewEncoder(os.Stdout)
	if err := enc.Encode(r); err != nil {
		fmt.Fprintf(os.Stderr, "emit: %v\n", err)
	}
	r.Summarize(os.Stderr)
}

func (r *Result) Summarize(w io.Writer) {
	if r.Error != "" {
		fmt.Fprintf(w, "  %-4s streams=%-3d FAILED: %s\n", r.Transport, r.Streams, r.Error)
		return
	}
	fmt.Fprintf(w,
		"  %-4s streams=%-3d chunk=%-7s gso=%-3s  %8.1f Mb/s  transfer=%6.2fs setup=%5.3fs  cpu=%.2fs/GiB\n",
		r.Transport, r.Streams, HumanBytes(r.ChunkBytes), r.GSO,
		r.Mbps, r.TransferSec, r.SetupSec, r.CPUSecPerGiB)

	for _, p := range r.Probes {
		fmt.Fprintf(w,
			"       %-14s %-4s  p50=%6.2fms p90=%6.2fms p99=%6.2fms max=%7.2fms  n=%d lost=%d\n",
			p.Kind, p.Phase, p.P50Ms, p.P90Ms, p.P99Ms, p.MaxMs, p.Samples, p.Lost)
	}
}

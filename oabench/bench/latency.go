package bench

import (
	"encoding/binary"
	"errors"
	"io"
	"math"
	"sort"
	"sync"
	"time"
)

// ProbeStats summarises one latency probe run. Percentiles rather than a mean:
// the question a probe answers is "how bad does interactive traffic get while
// bulk is running", and that lives in the tail.
type ProbeStats struct {
	Kind    string  `json:"kind"`  // quic-stream | quic-datagram | tcp-sepconn
	Phase   string  `json:"phase"` // idle | busy
	Samples int     `json:"samples"`
	Lost    int     `json:"lost"`
	P50Ms   float64 `json:"p50_ms"`
	P90Ms   float64 `json:"p90_ms"`
	P99Ms   float64 `json:"p99_ms"`
	MaxMs   float64 `json:"max_ms"`
}

const (
	probeInterval = 20 * time.Millisecond
	probeTimeout  = 2 * time.Second
	probePayload  = 8
	// idleProbeWindow is sampled before bulk starts, so every busy number has a
	// same-run, same-path baseline to be read against. Comparing a busy
	// percentile to anything else is comparing two different networks.
	idleProbeWindow = 2 * time.Second
)

var errProbeLost = errors.New("probe response not received")

// pingFunc issues one request/response exchange and returns its round trip.
type pingFunc func() (time.Duration, error)

// collectProbe samples until stop closes. A transport error ends the probe and
// keeps whatever was gathered -- the bulk transfer finishing will tear the
// probe channel down, and that is a normal end, not a failure.
func collectProbe(kind, phase string, ping pingFunc, stop <-chan struct{}) ProbeStats {
	st := ProbeStats{Kind: kind, Phase: phase}
	var samples []time.Duration

	ticker := time.NewTicker(probeInterval)
	defer ticker.Stop()

	for {
		select {
		case <-stop:
			return summarise(st, samples)
		case <-ticker.C:
			d, err := ping()
			switch {
			case err == nil:
				samples = append(samples, d)
			case errors.Is(err, errProbeLost):
				st.Lost++
			default:
				return summarise(st, samples)
			}
		}
	}
}

func summarise(st ProbeStats, s []time.Duration) ProbeStats {
	st.Samples = len(s)
	if len(s) == 0 {
		return st
	}
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })

	ms := func(d time.Duration) float64 {
		return math.Round(float64(d.Microseconds())/10) / 100
	}
	at := func(q float64) time.Duration {
		return s[int(math.Round(q*float64(len(s)-1)))]
	}

	st.P50Ms, st.P90Ms, st.P99Ms = ms(at(0.50)), ms(at(0.90)), ms(at(0.99))
	st.MaxMs = ms(s[len(s)-1])
	return st
}

// streamPing sends a sequence number and waits for it to come back. One
// exchange outstanding at a time, so the measurement is a clean round trip
// rather than a queue depth.
func streamPing(rw io.ReadWriter) pingFunc {
	buf := make([]byte, probePayload)
	var seq uint64
	return func() (time.Duration, error) {
		seq++
		binary.LittleEndian.PutUint64(buf, seq)
		start := time.Now()
		if _, err := rw.Write(buf); err != nil {
			return 0, err
		}
		if _, err := io.ReadFull(rw, buf); err != nil {
			return 0, err
		}
		return time.Since(start), nil
	}
}

// echoStream is the receiver side of streamPing.
func echoStream(rw io.ReadWriter) error {
	buf := make([]byte, probePayload)
	for {
		if _, err := io.ReadFull(rw, buf); err != nil {
			return err
		}
		if _, err := rw.Write(buf); err != nil {
			return err
		}
	}
}

// probeIdle samples a baseline before any bulk data moves. Results come back
// in a stable order regardless of goroutine scheduling, so JSON output diffs
// cleanly between runs.
func probeIdle(pings map[string]pingFunc) []ProbeStats {
	if len(pings) == 0 {
		return nil
	}
	stop := make(chan struct{})
	time.AfterFunc(idleProbeWindow, func() { close(stop) })
	return runProbes("idle", pings, stop)
}

// startBusyProbes begins sampling and hands back the means to stop and collect.
// Callers start it immediately before the transfer and finish it immediately
// after, so the samples cover the loaded period and nothing else.
func startBusyProbes(pings map[string]pingFunc) (chan<- struct{}, <-chan []ProbeStats) {
	if len(pings) == 0 {
		return nil, nil
	}
	stop := make(chan struct{})
	done := make(chan []ProbeStats, 1)
	go func() { done <- runProbes("busy", pings, stop) }()
	return stop, done
}

func finishBusyProbes(stop chan<- struct{}, done <-chan []ProbeStats) []ProbeStats {
	if stop == nil {
		return nil
	}
	close(stop)
	return <-done
}

func runProbes(phase string, pings map[string]pingFunc, stop <-chan struct{}) []ProbeStats {
	kinds := make([]string, 0, len(pings))
	for k := range pings {
		kinds = append(kinds, k)
	}
	sort.Strings(kinds)

	out := make([]ProbeStats, len(kinds))
	var wg sync.WaitGroup
	for i, k := range kinds {
		wg.Add(1)
		go func(i int, kind string) {
			defer wg.Done()
			out[i] = collectProbe(kind, phase, pings[kind], stop)
		}(i, k)
	}
	wg.Wait()
	return out
}

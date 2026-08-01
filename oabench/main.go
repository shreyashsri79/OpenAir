// Command oabench answers the OpenAir 2.0 week-1 transport question: can a
// single QUIC connection carrying N streams match v1.0's N-parallel-TCP engine,
// and at what CPU cost?
//
// PRD risk K1 says userspace QUIC may miss the throughput gate off Linux
// because Windows has no GSO. This harness is built so that question gets
// answered before any of the v2 architecture is built on top of QUIC.
//
//	oabench serve -transport quic
//	oabench send  -transport quic -streams 8 -size 1GiB
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/shreyashsri79/openair/oabench/bench"
)

func main() {
	log.SetFlags(log.Ltime)
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	switch os.Args[1] {
	case "serve":
		serve(os.Args[2:])
	case "send":
		send(os.Args[2:])
	case "mirror-serve":
		mirrorServe(os.Args[2:])
	case "mirror":
		mirror(os.Args[2:])
	case "help", "-h", "--help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand %q\n\n", os.Args[1])
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `oabench - OpenAir transport benchmark

  oabench serve -transport tcp|quic [-addr :9100] [-sink PATH]
  oabench mirror-serve [-addr :9100]
  oabench mirror [-addr host:9100] [-mode stream|datagram] [-fps 60]
                 [-bitrate 8Mbps] [-seconds 20] [-bulk] [-quiesce]
  oabench send  -transport tcp|quic [-addr host:9100] [-size 1GiB]
                [-streams 1,2,4,8] [-chunk 1MiB] [-runs 3] [-profile NAME] [-probe]

Results are one JSON object per line on stdout; a human summary goes to stderr.

Set QUIC_GO_DISABLE_GSO=1 to approximate the Windows send path on Linux.
`)
}

func serve(args []string) {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	transport := fs.String("transport", "quic", "tcp or quic")
	addr := fs.String("addr", ":9100", "listen address")
	sink := fs.String("sink", "", "reconstruct into this file via WriteAt (default: discard)")
	fs.Parse(args)

	var err error
	switch *transport {
	case "tcp":
		err = bench.ServeTCP(*addr, *sink)
	case "quic":
		err = bench.ServeQUIC(*addr, *sink)
	default:
		log.Fatalf("unknown transport %q", *transport)
	}
	if err != nil {
		log.Fatal(err)
	}
}

func send(args []string) {
	fs := flag.NewFlagSet("send", flag.ExitOnError)
	transport := fs.String("transport", "quic", "tcp or quic")
	addr := fs.String("addr", "127.0.0.1:9100", "receiver address")
	size := fs.String("size", "1GiB", "total bytes to transfer")
	chunk := fs.String("chunk", "1MiB", "chunk size (v1.0 clamps to 256KiB..4MiB)")
	streamList := fs.String("streams", "8", "comma-separated stream counts to sweep")
	runs := fs.Int("runs", 3, "runs per stream count; the median is reported")
	probe := fs.Bool("probe", false, "measure interactive latency idle and during the transfer")
	profile := fs.String("profile", "", "netem profile label recorded in the result")
	label := fs.String("label", "", "free-form label recorded in the result")
	streamWnd := fs.String("stream-window", "16MiB", "QUIC per-stream flow control window")
	connWnd := fs.String("conn-window", "64MiB", "QUIC per-connection flow control window")
	fs.Parse(args)

	totalBytes, err := bench.ParseBytes(*size)
	if err != nil {
		log.Fatal(err)
	}
	chunkBytes, err := bench.ParseBytes(*chunk)
	if err != nil {
		log.Fatal(err)
	}
	sw, err := bench.ParseBytes(*streamWnd)
	if err != nil {
		log.Fatal(err)
	}
	cw, err := bench.ParseBytes(*connWnd)
	if err != nil {
		log.Fatal(err)
	}

	counts, err := parseCounts(*streamList)
	if err != nil {
		log.Fatal(err)
	}

	for _, n := range counts {
		results := make([]*bench.Result, 0, *runs)
		for r := 0; r < *runs; r++ {
			cfg := bench.Config{
				Addr: *addr, Streams: n,
				ChunkBytes: chunkBytes, TotalBytes: totalBytes,
				Profile: *profile, Label: *label, Probe: *probe,
				StreamWindow: uint64(sw), ConnWindow: uint64(cw),
			}
			var res *bench.Result
			switch *transport {
			case "tcp":
				res = bench.RunTCP(cfg)
			case "quic":
				res = bench.RunQUIC(cfg)
			default:
				log.Fatalf("unknown transport %q", *transport)
			}
			if res.Error != "" {
				res.Emit()
				os.Exit(1)
			}
			results = append(results, res)
		}
		median(results).Emit()
	}
}

// median reports the middle run by throughput, which is more robust than a mean
// against a single scheduling hiccup.
func median(rs []*bench.Result) *bench.Result {
	sort.Slice(rs, func(i, j int) bool { return rs[i].Mbps < rs[j].Mbps })
	return rs[len(rs)/2]
}

func parseCounts(s string) ([]int, error) {
	parts := strings.Split(s, ",")
	out := make([]int, 0, len(parts))
	for _, p := range parts {
		n, err := strconv.Atoi(strings.TrimSpace(p))
		if err != nil {
			return nil, fmt.Errorf("parse stream count %q: %w", p, err)
		}
		if n < 1 {
			return nil, fmt.Errorf("stream count must be >= 1, got %d", n)
		}
		out = append(out, n)
	}
	return out, nil
}

// mirrorServe runs the D-9 spike's sink.
func mirrorServe(args []string) {
	fs := flag.NewFlagSet("mirror-serve", flag.ExitOnError)
	addr := fs.String("addr", ":9100", "listen address")
	fs.Parse(args)

	if err := bench.ServeMirror(*addr); err != nil {
		log.Fatal(err)
	}
}

// mirror runs the D-9 spike's source: one framing, one duration, one row of
// output. ADR-4 is decided on these numbers.
func mirror(args []string) {
	fs := flag.NewFlagSet("mirror", flag.ExitOnError)
	addr := fs.String("addr", "127.0.0.1:9100", "sink address")
	mode := fs.String("mode", "stream", "stream (PROTOCOL.md §14.2) or datagram (ADR-4 option A)")
	fps := fs.Int("fps", 60, "frames per second")
	bitrate := fs.String("bitrate", "8Mb", "encoded video bitrate, in bits per second (8Mb is a 1080p screen share)")
	seconds := fs.Int("seconds", 20, "how long to run")
	keyframe := fs.Int("keyframe", 60, "frames between keyframes")
	bulk := fs.Bool("bulk", false, "run a saturating transfer on the same connection (D-24's contention case)")
	quiesce := fs.Bool("quiesce", false, "throttle the bulk sender to a floor, as the session layer does")
	profile := fs.String("profile", "", "netem profile label recorded in the result")
	label := fs.String("label", "", "free-form label recorded in the result")
	fs.Parse(args)

	// The flag is in bits per second, because that is how video bitrate is
	// always quoted; everything below this line is bytes.
	rate, err := bench.ParseBytes(strings.TrimSuffix(strings.TrimSuffix(*bitrate, "ps"), "b") + "B")
	if err != nil {
		log.Fatalf("-bitrate: %v", err)
	}

	res := bench.RunMirror(bench.MirrorConfig{
		Addr:     *addr,
		Mode:     *mode,
		FPS:      *fps,
		Bitrate:  rate / 8,
		Seconds:  *seconds,
		Keyframe: *keyframe,
		Bulk:     *bulk,
		Quiesce:  *quiesce,
		Profile:  *profile,
		Label:    *label,
	})
	enc := json.NewEncoder(os.Stdout)
	if err := enc.Encode(res); err != nil {
		fmt.Fprintf(os.Stderr, "emit: %v\n", err)
	}
	res.Summarize(os.Stderr)
}

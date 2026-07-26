# oabench — OpenAir 2.0 week-1 transport spike

Answers one question before any v2 architecture gets built on top of QUIC:

> **Can a single QUIC connection carrying N streams match v1.0's N-parallel-TCP
> engine, and what does it cost in CPU?**

This is PRD risk **K1** ("userspace QUIC may miss the throughput gate off-Linux,
no GSO on Windows") and it gates **ADR-1**. The HLD's build order puts the
`oabench` gate at the *end* of Phase 1, after identity, session, files and
clipboard are built. That ordering means discovering a wrong transport after
four layers sit on it. This harness exists so the gate runs first.

## Quick start

```bash
go build -o oabench .

# shaped, reproducible, no root required
./netem/matrix.sh                        # lan-1g, wifi-5g, wan-relay
SIZE=1GiB RUNS=5 ./netem/matrix.sh wifi-5g

# or drive it by hand
./oabench serve -transport quic -addr :9100
./oabench send  -transport quic -addr 127.0.0.1:9100 -size 1GiB -streams 1,4,8
```

Results are JSON Lines on stdout, human summary on stderr.
`./netem/report.sh results.jsonl` pivots them into a gate table.

## What it measures

| Field | Why it's here |
|---|---|
| `mbps` | Application goodput. The headline number. |
| `cpu_sec_per_gib` | **The number that predicts Windows.** QUIC is userspace: it pays per-packet crypto and syscall costs the kernel absorbs for TCP. Matching throughput while burning 10x the CPU means the wall hasn't been hit yet, not that there isn't one. |
| `setup_sec` | QUIC's 1-RTT handshake vs TCP's 3-way plus N dials. Noise for bulk transfer, but it's the whole cost for a clipboard push. |
| `probes[]` | With `-probe`: interactive round-trip percentiles, sampled idle then during the transfer. The idle row is the same-run baseline; the busy row is what a user feels while a file is moving. Requires reading the pair, never the busy number alone. |
| `transfer_sec` | First byte sent until the receiver acknowledges the last byte. |

## What Linux alone can and cannot answer

The spike's whole point is de-risking a Windows problem from a Linux box. Being
precise about the limits of that:

### Answerable on Linux

**Transport efficiency under a realistic path.** `netem/lab.sh` shapes rate,
RTT and loss, so the measurement reflects congestion control and recovery rather
than an unshaped loopback fantasy. (Unshaped loopback reports ~38 Gb/s for TCP.
Any number taken without shaping is worthless.)

**The GSO question, by proxy.** quic-go batches sends via `UDP_SEGMENT`
(generic segmentation offload) on Linux. Windows has no equivalent, so quic-go
sends one packet per syscall there. Setting `QUIC_GO_DISABLE_GSO=1` forces that
same one-packet-per-syscall path on Linux. It is not Windows, but it isolates
exactly the variable that makes Windows different, and it is the dominant term.

**CPU cost per byte.** Directly measured via `getrusage`. This transfers across
platforms far better than throughput does.

**Whether parallel streams still help.** v1.0's core trick was N TCP connections
to defeat per-flow throttling. Whether that trick survives being remapped onto N
streams inside *one* QUIC connection is a pure protocol question, fully
answerable here — and the answer changes the HLD (see Findings).

### Not answerable on Linux

**Absolute Windows throughput.** Windows' UDP stack, syscall cost and
scheduler differ beyond what GSO-off simulates. GSO-off Linux is a *lower
bound proxy*, not a prediction. Confirming K1 needs one real run on the
Windows laptop — the harness cross-compiles, so that's `GOOS=windows go build`
and one afternoon.

**Real CGNAT punch success rates.** Needs actual Jio/Airtel paths and two real
devices. netem can simulate NAT behaviour for testing punch *logic* in CI, but
not carrier policy. That is a separate spike.

**gomobile binding and APK size.** Needs the Android NDK. What *is* confirmed:
`CGO_ENABLED=0 GOOS=android GOARCH=arm64 go build` succeeds for quic-go, so the
transport is portable to Android as pure Go. Binding friction and APK size —
the actual ADR-5 tradeoff — remain open.

**Co-location distortion.** Sender, receiver and netem all share one CPU here.
That inflates CPU contention and penalises the CPU-hungrier transport, so
GSO-off QUIC numbers are pessimistic. The *ratio* of CPU per byte between
transports is sound; the absolute throughput under GSO-off is a floor.

## Fairness notes

The baseline has to be TCP at its best, not a strawman, or the comparison
proves nothing:

- **Identical framing.** Both transports use v1.0's exact 12-byte chunk header
  (`int64` offset + `int32` size, little-endian). Only the transport varies.
- **One write per chunk.** Header and payload share a buffer. v1.0 issued three
  writes per chunk (two `binary.Write` calls plus data); reproducing that would
  have handicapped the baseline.
- **No logging in the hot path.** v1.0's sender `Printf`s several times per
  chunk. At 1 MiB chunks and 1 Gb/s that is thousands of formatted writes per
  second — it would have made TCP look far worse than it is.
- **Kernel socket buffers left on autotune** for TCP; QUIC flow-control windows
  raised to 16 MiB/stream and 64 MiB/connection, because quic-go's defaults
  throttle a high bandwidth-delay-product path and would have measured default
  flow control rather than the transport.
- **Median of N runs**, not mean, so one scheduling hiccup doesn't set the
  number.
- **Delivery-timed, not buffer-timed.** The clock stops when the *receiver*
  acknowledges the final byte on a separate control channel. Stopping when the
  last `Write` returns would measure the kernel send buffer.
- **Structural fidelity.** TCP runs N independent connections, as v1.0 does.
  QUIC runs one connection with N streams, as the HLD's "one connection per
  peer" principle requires. That asymmetry is the point of the experiment, not
  a flaw in it.

## The lab

`netem/lab.sh` runs a command inside a private network namespace with a shaped
loopback, using an **unprivileged user namespace** — no root, no sudo. `lo` is
set to MTU 1500 so packet counts are realistic.

Loopback rather than a veth pair is a tradeoff: veth is more faithful but moving
an interface between namespaces needs real root. Shaped loopback still exercises
the full UDP socket path including `UDP_SEGMENT`, which is what's under test.

Every profile is an **emulation** — a rate limit, a fixed delay and a uniform
random loss rate imposed on loopback. The namespace holds no interface but `lo`
and has no route off it, so nothing here touches real hardware. Verify with:

```bash
./netem/lab.sh wifi-5g -- ip -brief link show   # only lo
./netem/lab.sh wifi-5g -- ip route show         # empty
```

Queue depth is derived from the bandwidth-delay product — `BUFFER_BDP` multiples,
default 4, which is consumer-typical. Set `BUFFER_BDP=1` for a shallow-buffered
link or a large value to study bufferbloat on purpose. This matters: an earlier
revision used a flat 100000-packet queue, about 150 MB, and latency-under-load
measured the lab rather than the transport.

| Profile | Models this link | Rate | RTT | Loss |
|---|---|---|---|---|
| `lan-1g` | Wired gigabit Ethernet between two desktops | 1 Gb/s | 1 ms | 0% |
| `hotspot` | Phone tethering / device-to-device AP, no infrastructure hop — v1.0's 477 Mb/s case | 600 Mb/s | 4 ms | 0.05% |
| `wifi-5g` | 5 GHz WiFi through a home access point — v1.0's 148 Mb/s case | 200 Mb/s | 6 ms | 0.1% |
| `wan-relay` | Cross-network hop via a VPS relay, both peers behind NAT (PRD R7/R8) | 100 Mb/s | 80 ms | 0.5% |
| `cgnat-punch` | Mobile data behind carrier-grade NAT (PRD K2) | 50 Mb/s | 120 ms | 1% |

**Fidelity limit:** netem is a token bucket plus fixed delay and uniform random
loss. Real WiFi has contention, rate adaptation and bursty loss; a real relay
has jitter and competing traffic. Absolute numbers are indicative of the
modelled link, not predictive of it. The *comparison* holds regardless, because
both transports run back-to-back under byte-identical conditions — any error in
the model applies equally to each.

## Findings

Full write-up in `docs/decision-tree.md` **D-4**. Headline, 192 MiB, median of 2,
1 MiB chunks, single machine.

**Every condition below is emulated** with `tc netem` on an isolated loopback —
no physical NIC, WiFi radio, hotspot or WAN link was involved. Profile names say
which real-world link each preset models. Throughput in Mb/s:

| profile | models this link | rate / RTT / loss | TCP best | QUIC GSO on | QUIC GSO off |
|---|---|---|---|---|---|
| `lan-1g` | wired gigabit Ethernet | 1 Gb/s / 1 ms / 0% | 952 | 660 | 446 |
| `hotspot` | phone tethering, device-to-device | 600 Mb/s / 4 ms / 0.05% | 568 | 559 | 179 |
| `wifi-5g` | 5 GHz WiFi via a home AP | 200 Mb/s / 6 ms / 0.1% | 188 | 185 | 93 |
| `wan-relay` | VPS relay, both peers behind NAT | 100 Mb/s / 80 ms / 0.5% | **27.4** | **5.5** | 3.3 |

**1. Parallel streams inside one QUIC connection do not replace parallel TCP
connections.** On the relayed profile, TCP scales 3.75 → 27.4 Mb/s across 1→8
connections. QUIC is flat at ~5.5 Mb/s across 1→8 streams. One connection is
one congestion controller, and quic-go v0.61 ships only Cubic — loss-based,
`internal/`, no BBR, no exported knob. At 0.5% loss a single Cubic flow is
pinned near the Mathis limit; TCP escapes it by running N flows.

QUIC actually *wins* single-flow (5.5 vs 3.75). It loses only because it cannot
be parallelised the way v1.0 parallelises TCP.

This is the result that matters, and it is the one **not** confounded by the
single-machine lab: `wan-relay` runs at 3–27 Mb/s, nowhere near CPU-bound, and
flat stream-scaling is a property of the congestion controller, not the rig.

**2. K1 is real but is not the first problem.** Disabling GSO costs 50–70% of
QUIC throughput and triples CPU on every profile. Worth a real Windows run —
after (1) is resolved.

**3. QUIC costs 10–20× the CPU per byte** (15–25 s/GiB vs TCP's 0.3–1.7). That
is a live risk for PRD R30's Android battery and <50 MB RSS desktop budgets,
and feeds ADR-5.

**Caveat on the gigabit rows:** sender, receiver and netem share one core, so
`lan-1g` is CPU-bound and understates QUIC. Two-machine runs are needed before
those rows mean anything.

## Status

Spike code. It is expected to graduate to `cmd/oabench` in the v2 tree — the
HLD already calls the benchmark harness a first-class tool — but `bench/` is
written for measurement, not production: the TLS identity uses a **fixed
Ed25519 seed** so the client can pin the server without an out-of-band
exchange. That must not survive into the daemon.

The pinning path itself is real, and doubles as an ADR-2 dry run: an Ed25519
device key serving as the TLS 1.3 certificate key, with peer verification by
raw public key via `VerifyPeerCertificate` and no CA anywhere. It works, which
is most of the evidence ADR-2 needs.

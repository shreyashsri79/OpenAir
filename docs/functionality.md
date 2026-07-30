# Functionality Map

Living doc. Update whenever you add/move/rename a file or change control flow/protocol — same commit as the code change. See AGENTS.md for rules. If this file and the code disagree, code wins — fix this file.

Core model (all modules): sender splits a file into offset-tagged chunks, opens multiple parallel TCP connections, streams chunks concurrently; receiver writes each chunk via `file.WriteAt(data, offset)` for out-of-order parallel reconstruction. Discovery via mDNS (`_openair._tcp`). Receiver does explicit Accept/Reject on incoming transfer metadata.

## openair-android (Kotlin)
Purpose: Android app — device discovery, sending, and receiving over the OpenAir protocol.
- `core/NsdDiscoveryManager.kt` — mDNS/NSD peer discovery on Android.
- `core/OpenAirReceiver.kt` — receive-side socket handling on Android.
- `core/OpenAirSender.kt` — send-side chunked streaming on Android.
- `OpenAirViewModel.kt` / `OpenAirUiState.kt` — app state (Compose ViewModel + UI state model).
- `ui/OpenAirScreen.kt`, `ui/OpenAirContent.kt`, `ui/components/*` — Compose UI (device list, file chips, transfer progress, hand-drawn toggle/marker styling).
- `MainActivity.kt` — entry point.

## openair-gui (Go, Fyne)
Purpose: Desktop GUI (Linux/macOS/Windows) wrapping the Go transfer engine.
- `main.go` — app entry, wires UI to sender/receiver.
- `theme.go` — Fyne custom theme.
- `ziputil.go` — packaging helper (build/dist zipping), see `build_release.sh`.
- `internal/receiver/receiver.go` — receive-side logic used by the GUI.
- `internal/sender/sender.go` — send-side logic used by the GUI.
- `internal/sender/discover.go` — peer discovery for GUI sender (replaces old `discoverAndroid.go`, removed — see decisions log if a rationale entry exists, otherwise log why on next touch).

## docs/BUILD-PLAN.md — execution plan
Purpose: the whole project split into 15 milestones plus 6 cross-cutting tracks, each independently runnable so no milestone waits on a later one to be useful. Carries a complexity tier per task (T1 mechanical / T2 standard / T3 deep) for model assignment, an 8-wave parallelisation map, and — per task — the exact document sections an agent should read and the ones it should not. Start here when picking up work.

## v2 tree (Go, root module `github.com/shreyashsri79/openair`)
Purpose: OpenAir 2.0. Per D-26 this is one module; `openair-gui` and `oabench` stay separate until v1.0 retires. Currently a skeleton — every package has a `doc.go` stating its responsibility and the decisions governing it, and nothing else. No behaviour yet.
- `cmd/openaird` — the always-on daemon.
- `cmd/openair` — CLI client over local IPC (D-29: the session envelope, not gRPC).
- `internal/identity` — two keypairs (D-20), at-rest sealing (D-19), unlock sessions (D-18, D-30), protection tiers (D-21), trust store.
- `internal/discovery` — mDNS `_openair._udp` plus unicast fallback. Emits candidates, never dials.
- `internal/conn` — one QUIC connection per peer (D-6); Phase 2 adds path racing and migration.
- `internal/session` — envelope framing, capability negotiation, authorisation middleware, quiesce arbitration (D-24).
- `internal/caps/{files,clipboard}` — Phase 1 capabilities.
- `internal/wire` — generated protobuf, committed (D-28).

## proto/ and internal/wire/ — executable wire schemas
Purpose: `docs/PROTOCOL.md` in compilable form. `proto/openair/v1/` holds one file per capability plus `common.proto` for shared enums; `internal/wire/` holds committed generated Go (D-28). Root `buf.yaml` defines the workspace, `buf.gen.yaml` the codegen. `buf lint` uses STANDARD; `buf breaking` guards PRD R32's mixed-version compatibility mechanically.
- `input` (capID 5) has no schema by design — raw datagram encoding, PROTOCOL.md section 13.
- Bulk chunk frames have no schema either: a `files` data stream opens with `StreamInit` then carries raw 12-byte-header frames inherited from v1.0.
- **Enum values are offset by one from wire values**, because proto3 reserves 0 for `UNSPECIFIED`. Encoders convert; they must not cast.
- Writing these found six defects in the spec, corrected in the same commit — see D-34.

## docs/PROTOCOL.md — normative wire specification
Purpose: the versioned wire format, spec-first per HLD principle 4. Covers all four phases: envelope, session establishment, pairing, authorisation and flow control; `files` and `clipboard` (Phase 1); discovery, rendezvous, relay and connection establishment (Phase 2); `remotefs` and `notifications` (Phase 3); `input` and `mirror` (Phase 4). Section 14 (`mirror`) is provisional pending D-9. Where code and this file disagree, the spec is right and the code is a bug. No implementation exists yet.

## oabench (Go) — v2.0 transport spike
Purpose: measure whether a single QUIC connection with N streams can match v1.0's N-parallel-TCP engine, and at what CPU cost. Exists to settle PRD risk K1 / ADR-1 **before** the v2 stack is built on QUIC. Own Go module (`github.com/shreyashsri79/openair/oabench`); does not import or affect `openair-gui`.
- `main.go` — `serve` / `send` subcommands, flag parsing, stream-count sweep, median-of-N.
- `bench/framing.go` — v1.0's 12-byte chunk header (`int64` offset + `int32` size, little-endian), role tags, session preamble. Deliberately byte-identical to v1.0 so the measurement isolates transport, not framing.
- `bench/plan.go` — lock-free chunk distribution (atomic counter replaces v1.0's jobs channel) and payload generation.
- `bench/transfer.go` — `Config`, shared send loop, and the sink (discard, or `WriteAt` reconstruction).
- `bench/tcp.go` — v1.0-shaped baseline: N independent TCP connections.
- `bench/quic.go` — v2.0-shaped candidate: one QUIC connection, N streams, per HLD "one connection per peer".
- `bench/tlsutil.go` — Ed25519 identity, self-signed cert, `VerifyPeerCertificate` pinning. Doubles as the ADR-2 dry run.
- `bench/latency.go` — interactive-latency probe: request/response ping sampled idle then during the transfer, reported as percentiles. `probeIdle` / `startBusyProbes` / `finishBusyProbes` orchestrate the two phases; `streamPing` and `echoStream` are transport-agnostic, `datagramPing` and `echoDatagrams` live in `quic.go`.
- `bench/cpu_linux.go` / `cpu_other.go` — `getrusage` CPU accounting; no-op off Linux.
- `netem/lab.sh` — runs a command in an unprivileged user+network namespace with `tc netem` applied to a 1500-MTU loopback. No root needed.
- `netem/run-one.sh` — one profile's sweep: TCP, then QUIC with GSO on, then GSO off.
- `netem/matrix.sh`, `netem/report.sh` — full matrix runner and the gate pivot table.

Control flow (both transports): sender opens a control channel, sends `preamble{total, streams, chunk, flags}`, waits for a go-ahead, then opens N data channels. With `-probe`, a ping channel is opened *before* the go-ahead — a QUIC stream on the same connection as bulk, or for TCP a separate connection, mirroring v1.0's architecture. That asymmetry is the measurement, not an oversight (D-17). Timer starts once all channels are up and stops when the **receiver** acks completion on the control channel — stopping at the last `Write` would measure the kernel send buffer, not delivery. On QUIC the receiver must drain the control stream to EOF before closing the connection, because `CONNECTION_CLOSE` preempts undelivered stream data and would race the ack away.

Sharp edges:
- `bench/tlsutil.go` uses a **fixed Ed25519 seed** so the client can pin the server with no out-of-band exchange. Spike only; must not reach the daemon.
- Timing is only meaningful under `netem/lab.sh`. Unshaped loopback reports ~38 Gb/s for TCP.
- Sender, receiver and netem share one CPU, so results are pessimistic for the CPU-hungrier transport (QUIC) and become CPU-bound rather than link-bound at gigabit rates. Two-machine runs are needed before ADR-1 is settled.
- `QUIC_GO_DISABLE_GSO=1` was originally used as a Linux proxy for the Windows send path. Per D-13 it is not a proxy: quic-go's `UDP_SEGMENT` support is Linux-only by construction, so GSO-off *is* Windows and macOS behaviour.
- netem queue depth is derived from the bandwidth-delay product, `BUFFER_BDP` multiples, default 4. It was previously a flat 100000 packets, which was extreme bufferbloat and made latency-under-load meaningless; see D-17. Always state the buffer depth when quoting latency figures.

## Removed modules
`openair-cli`, `openair-receiver`, `openair-sender` deleted — see `docs/decision-tree.md` D-2. Project scope is now gui + android only. `openair-gui/internal/sender` and `internal/receiver` are the sole Go implementation.

## Sharp edges / open questions (fill in as discovered)
- Chunk wire format (offset/size/data framing) and handshake message format are described at a high level in README but not yet pinned down file-by-file here — next agent touching the protocol should fill this in from `openair-gui/internal/sender` and `internal/receiver`.

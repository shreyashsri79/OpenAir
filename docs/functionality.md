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

## oabench (Go) — v2.0 transport spike
Purpose: measure whether a single QUIC connection with N streams can match v1.0's N-parallel-TCP engine, and at what CPU cost. Exists to settle PRD risk K1 / ADR-1 **before** the v2 stack is built on QUIC. Own Go module (`github.com/shreyashsri79/openair/oabench`); does not import or affect `openair-gui`.
- `main.go` — `serve` / `send` subcommands, flag parsing, stream-count sweep, median-of-N.
- `bench/framing.go` — v1.0's 12-byte chunk header (`int64` offset + `int32` size, little-endian), role tags, session preamble. Deliberately byte-identical to v1.0 so the measurement isolates transport, not framing.
- `bench/plan.go` — lock-free chunk distribution (atomic counter replaces v1.0's jobs channel) and payload generation.
- `bench/transfer.go` — `Config`, shared send loop, and the sink (discard, or `WriteAt` reconstruction).
- `bench/tcp.go` — v1.0-shaped baseline: N independent TCP connections.
- `bench/quic.go` — v2.0-shaped candidate: one QUIC connection, N streams, per HLD "one connection per peer".
- `bench/tlsutil.go` — Ed25519 identity, self-signed cert, `VerifyPeerCertificate` pinning. Doubles as the ADR-2 dry run.
- `bench/cpu_linux.go` / `cpu_other.go` — `getrusage` CPU accounting; no-op off Linux.
- `netem/lab.sh` — runs a command in an unprivileged user+network namespace with `tc netem` applied to a 1500-MTU loopback. No root needed.
- `netem/run-one.sh` — one profile's sweep: TCP, then QUIC with GSO on, then GSO off.
- `netem/matrix.sh`, `netem/report.sh` — full matrix runner and the gate pivot table.

Control flow (both transports): sender opens a control channel, sends `preamble{total, streams, chunk}`, waits for a go-ahead, then opens N data channels. Timer starts once all channels are up and stops when the **receiver** acks completion on the control channel — stopping at the last `Write` would measure the kernel send buffer, not delivery. On QUIC the receiver must drain the control stream to EOF before closing the connection, because `CONNECTION_CLOSE` preempts undelivered stream data and would race the ack away.

Sharp edges:
- `bench/tlsutil.go` uses a **fixed Ed25519 seed** so the client can pin the server with no out-of-band exchange. Spike only; must not reach the daemon.
- Timing is only meaningful under `netem/lab.sh`. Unshaped loopback reports ~38 Gb/s for TCP.
- Sender, receiver and netem share one CPU, so results are pessimistic for the CPU-hungrier transport (QUIC) and become CPU-bound rather than link-bound at gigabit rates. Two-machine runs are needed before ADR-1 is settled.
- `QUIC_GO_DISABLE_GSO=1` is the Linux proxy for the Windows send path; it is a lower bound, not a prediction.

## Removed modules
`openair-cli`, `openair-receiver`, `openair-sender` deleted — see `docs/decision-tree.md` D-2. Project scope is now gui + android only. `openair-gui/internal/sender` and `internal/receiver` are the sole Go implementation.

## Sharp edges / open questions (fill in as discovered)
- Chunk wire format (offset/size/data framing) and handshake message format are described at a high level in README but not yet pinned down file-by-file here — next agent touching the protocol should fill this in from `openair-gui/internal/sender` and `internal/receiver`.

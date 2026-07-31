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
Purpose: OpenAir 2.0. Per D-26 this is one module; `openair-gui` and `oabench` stay separate until v1.0 retires. **M1 (direct transfer) and M2 (pairing and trust store) are implemented and work end to end** — two devices pair once, and a file then moves between them over QUIC with its SHA-256 matching. No discovery (M3), no daemon (M4).

- `cmd/openair` — the CLI. `pair --listen ADDR` displays an offer and waits; `pair OFFER` consumes one that was scanned or typed. `recv` listens and writes inbound files; `send` dials an explicit `host:port` and offers files. M1's fingerprint prompt is gone from both transfer paths: the trust store answers that question now, and an unpaired device is refused at both ends rather than prompted about. `--yes` still skips the *transfer* prompt on `recv`; there is deliberately no flag that skips the pairing digits (§5.2 forbids one). `main.go` dispatches, `common.go` holds key/trust-store loading and the fingerprint/confirm helpers, `pair.go`, `recv.go` and `send.go` are the three flows. Long term this becomes a client for the daemon over local IPC (D-29); it drives sessions directly only because M4 does not exist yet.
- `cmd/openaird` — the always-on daemon. Still a stub; M4.
- `internal/identity` — two Ed25519 keypairs (D-20). `deviceid.go` derives the DeviceID (§2). `tlsconfig.go` is the D-7 pinning mechanism ported from `oabench/bench/tlsutil.go`: self-signed cert whose key *is* the identity key, raw-public-key comparison, no CA and no hostname check, with ALPN and TLS version re-checked in `VerifyConnection` because Go negotiates an empty ALPN rather than failing when a client offers none. `keyfile.go` is the Appendix A sealed container (XChaCha20-Poly1305 + Argon2id); `truststore.go` is the concurrent-safe persisted store. Unlock (M6) is stubbed: `SignOwned` always returns `ErrLocked`.
- `internal/conn` — dial and accept over one QUIC connection per peer (D-6). `quic_config.go` carries the window and timeout values measured in `oabench`. `DialAddr` pins the peer's key; `Listener` cannot, so it gates callers through the `Authorize` callback instead (D-37). Both install BBR immediately after the handshake (D-36). `Dial` by DeviceID returns a not-implemented error — that is Phase 2.
- `internal/session` — `envelope.go` is the 8-byte frame (§3), validating version and length *before* allocating so an 8-byte header claiming 4 GiB costs the receiver nothing. `session.go` is the concrete `Session`: control stream, Hello exchange (§4), capability negotiation, and dispatch through one bounded queue per capability (D-41). `convert.go` holds every wire↔domain conversion in one place (D-34, D-39). `testdata/envelope_vectors.json` + `golden_test.go` are the hand-derived golden vectors (HLD §5).
- `internal/caps/files` — the `files` capability (capID 1). `plan.go` distributes chunks by atomic counter and validates inbound frames; `framing.go` is v1.0's 12-byte chunk header; `send.go` and `recv.go` are the two sides; `state.go` persists the verified-chunk bitmap for resume; `path.go` refuses traversal before any filesystem call is made.
- `internal/pairing` — PROTOCOL.md §5 (pairing), §6.1 (revocation) and §6.4 (grants), all on capID 0. `offer.go` is the out-of-band offer: one base32 string carrying the DeviceID, a 16-byte fingerprint and LAN hints, printed as `openair://pair/...` for a QR and hyphenated for typing; `VerifyOffer` checks the key the peer presented in TLS against the scanned fingerprint, which is what authenticates the offering device. `sas.go` derives the six digits both users compare — see D-45 for the three things §5.2 left unstated. `pairing.go` runs the exchange (`Initiate` scanned-and-dialled, `Await` displayed-and-accepted); it binds the identity key in every pairing message to the key that terminated TLS, so a claimed key can never be the one that gets pinned. `authorize.go` is the trust-store gate that replaces M1's nil `session.Config.Authorize`, plus the pairing window that is the *only* thing admitting an unpaired peer. `guard.go` is §6.1's mid-session enforcement point: a live session consults it per operation, so a revoke lands on work already in progress.
- `internal/congestion` — BBR, vendored from Hysteria (D-35) and installed on live connections through the apernet fork's public `SetCongestionControl`. `PROVENANCE.md` records the upstream, the two local modifications and the re-sync procedure. Default gain profile is conservative (D-36).
- `internal/discovery` — mDNS `_openair._udp` plus unicast fallback. Still a stub; M3.
- `internal/caps/clipboard` — stub; M5.
- `internal/wire` — generated protobuf, committed (D-28).
- `internal/{identity,session,caps,conn}/types.go` — the shared Phase 1 contracts (`DeviceID`, `Identity`, `TrustStore`, `Envelope`, `Session`, `Stream`, `Capability`, `Dialer`). Defined ahead of implementation so parallel agents could not invent incompatible boundaries; they survived wave 1 with one addition (`Config.Authorize`, D-37).

Control flow, `send` to `recv`: dial → QUIC/TLS handshake with the peer's raw key pinned (or unpinned at M1) → install BBR → open the control stream and exchange Hello → `Authorize` gates the peer → offer (`TransferOffer`) → receiver accepts and returns any verified-chunk set for resume → manifest of per-chunk digests → **two** data streams (never eight — D-13, D-33) carrying raw 12-byte-header frames → receiver verifies each chunk against the manifest *before* writing → rename `.oapart` into place → `TransferComplete`.

Sharp edges:
- **A chunk offset is transfer-global across the offered files concatenated in offer order, and chunks never span a file boundary** (D-40). PROTOCOL.md §8.3 does not say this yet and needs the amendment — it is wire-visible.
- Two data streams by default. `TestDefaultStreamCountIsTwo` exists to stop v1.0's eight coming back.
- A receiver verifies before writing, so corrupt bytes never reach the destination file. `Plan.Locate` doubles as an authorisation check: without it a peer granted `files` could write arbitrary bytes at an arbitrary offset by lying in a 12-byte header.
- `conn.Listener.Accept` runs the Hello exchange inline, so a peer that completes the QUIC handshake and then says nothing holds up the accept loop until the idle timeout. The CLI works around it by accepting in a background goroutine; the daemon (M4) needs a real fix.
- A nil `session.Config.Authorize` still admits every peer — the type permits it. Nothing in `cmd/openair` passes nil any more (M2 wires `pairing.Handler.Authorize` on every path), but the hazard is now that a *future* caller can forget to, and the compiler will not say so. Making it non-optional on the listening path is still owed.
- **A message that ends the conversation must not be followed straight by a close** — `CONNECTION_CLOSE` overtakes unflushed stream data and the peer never sees it (D-46). `internal/pairing` lingers 250 ms; the general fix is a flush-then-close on `session.Session`, which M4 will want.
- Pairing admits an unpaired peer only while a window is open, and windows nest. A UI that opens one and forgets to close it leaves the device pairable indefinitely — `pair --listen` scopes it to the life of the command.
- `identity.ProtectionTier` runs 0/1/2 with `TierNone` first, while the wire runs 1/2/3 in the opposite order. Convert, never cast — a cast downgrades a keystore-backed peer to unprotected (D-39).
- `send` reads each source file twice: once for the offer and manifest digests, once to transmit. §8.4 permits a single pass by sending the manifest during the transfer, at the cost of buffering unverified chunks. Deliberate.
- The privilege *public* key lives in a `privilege.pub` sidecar because Appendix A has no field for it (D-42).

## proto/ and internal/wire/ — executable wire schemas
Purpose: `docs/PROTOCOL.md` in compilable form. `proto/openair/v1/` holds one file per capability plus `common.proto` for shared enums; `internal/wire/` holds committed generated Go (D-28). Root `buf.yaml` defines the workspace, `buf.gen.yaml` the codegen. `buf lint` uses STANDARD; `buf breaking` guards PRD R32's mixed-version compatibility mechanically.
- `input` (capID 5) has no schema by design — raw datagram encoding, PROTOCOL.md section 13.
- Bulk chunk frames have no schema either: a `files` data stream opens with `StreamInit` then carries raw 12-byte-header frames inherited from v1.0.
- **Enum values are offset by one from wire values**, because proto3 reserves 0 for `UNSPECIFIED`. Encoders convert; they must not cast.
- Writing these found six defects in the spec, corrected in the same commit — see D-34.

## .github/workflows — CI
Purpose: mechanically enforce the build plan's definition of done (D-44).
- `ci.yml` — `gofmt -l`, then build/vet/`test -race`/`GOOS=windows build` across both Go modules. The Windows cross-build is a hard gate per D-32, so the platform cannot rot while its milestone is deferred.
- `buf.yml` — `buf lint` and `buf breaking`, guarding PRD R32's mixed-version compatibility.
- `netem.yml` — the shaped-network matrix, `workflow_dispatch` only. Hosted runners block `unshare -Urn` by AppArmor default; the sysctl workaround is present but unverified, and a nightly job that is always red is worse than none.
- Not gated: `openair-gui` (needs system GL/X11 packages), `buf generate` reproducibility, and the end-to-end-on-real-hardware condition, which no CI job can substitute for.

## docs/threat-model.md — PRD R29
Purpose: assembles the security reasoning scattered across the decision log and PROTOCOL.md into something a reader can evaluate whole. Assets, five trust boundaries, eight adversaries, what rendezvous and relay operators each learn, accepted weaknesses, and non-goals. Every claim cites a section or decision. §7 is the part to read: ten open questions, four of which need a maintainer decision (D-43).

## docs/PROTOCOL.md — normative wire specification
Purpose: the versioned wire format, spec-first per HLD principle 4. Covers all four phases: envelope, session establishment, pairing, authorisation and flow control; `files` and `clipboard` (Phase 1); discovery, rendezvous, relay and connection establishment (Phase 2); `remotefs` and `notifications` (Phase 3); `input` and `mirror` (Phase 4). Section 14 (`mirror`) is provisional pending D-9. Where code and this file disagree, the spec is right and the code is a bug.

**Nine outstanding edits**, all found by implementing Phase 1 and worked around rather than fixed. M2 added four: §5.2 never states that the pairing transcript sorts both key pairs by value, gives no encoding for a tier-none device's absent privilege key, and is ambiguous about which nonce comes first (D-45); and §6.4's prose still carries its own trust scale where the schema now uses the shared `TrustLevel` enum. The five from wave 1: §8.3 must state that a chunk offset is transfer-global across concatenated files (D-40) — this one is wire-visible and urgent. §3 and §10 disagree on the error code for an unknown envelope version (D-38). §3's "every enum is offset by one" is wrong for `msgType` and `ProtectionTier` (D-39). Appendix A needs a field for the privilege public key (D-42). §17's `RelayAuth` needs domain separation and a binding to the relay's identity (D-43). Two more places are underspecified rather than wrong: §6's `AuthProof` signing input gives no widths or endianness, and §2 never states that the handshake is mutually authenticated — a responder following it literally leaves the initiator unauthenticated, which §6's model assumes cannot happen.

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

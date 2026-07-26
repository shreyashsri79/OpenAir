# Decision Log

Append-only. See AGENTS.md for format and rules. Do not delete or rewrite past entries.

## D-1: Adopt AGENTS.md + decision log convention
Date: 2026-07-25
Status: accepted
Context: Multiple agents work this repo concurrently; decisions and code logic were living only in commit messages/chat, not discoverable by a fresh agent or contributor.
Decision: Introduce `AGENTS.md` (agent rules), `docs/decisions.md` (this file, ADR-style log), and `docs/functionality.md` (living code map). All agents must read/update these going forward.
Alternatives considered: Rely on commit messages only (rejected — not searchable/summarized, no rationale trail); full ADR tool/format like adr-tools (rejected — overkill for project size, plain markdown is enough).
Consequences: Every future non-trivial decision must be logged here; functionality.md must be kept in sync with code changes.

## D-2: Drop openair-cli, openair-receiver, openair-sender; focus on gui + android only
Date: 2026-07-25
Status: accepted
Context: Repo had four parallel/duplicated Go implementations (standalone sender, standalone receiver, cli, and gui's own internal/sender+receiver) per D-1's open question in functionality.md — unclear ownership, redundant maintenance surface. Project direction narrowed to just the desktop GUI and Android app.
Decision: Delete `openair-cli/`, `openair-receiver/`, `openair-sender/` and their release artifacts (`release/cli/`) entirely. Remove the CLI cross-compile block from `build_release.sh`. `openair-gui` (with its own `internal/sender`, `internal/receiver`) and `openair-android` are now the only supported clients.
Alternatives considered: Keep CLI as a thin wrapper around gui's internal packages (rejected — not asked for, adds scope); archive instead of delete (rejected — user asked for deletion, git history preserves it if needed).
Consequences: `build_release.sh` only builds gui + android. Any protocol/library code shared logic previously duplicated in cli/receiver/sender now lives solely in `openair-gui/internal/*`.

## D-3: Run the v2 transport gate first, before any of the v2 stack
Date: 2026-07-25
Status: accepted
Context: The HLD build order (section 7) puts the `oabench` benchmark gate at the *end* of Phase 1 — after identity, trust store, session, files and manual clipboard. But ADR-1 (transport = QUIC) is marked accepted while PRD risk K1 says userspace QUIC may miss the throughput gate off-Linux. If K1 turns out to be real, that ordering means discovering the foundation is wrong after four layers have been built on it. The whole v2 architecture rests on QUIC; nothing else in Phase 1 is load-bearing in the same way.
Decision: Build `oabench/` as the first piece of v2 work and run the transport gate before writing identity, session or capability code. Harness measures v1.0's N-parallel-TCP engine against a single QUIC connection with N streams, under a shaped network, reporting both goodput and CPU per byte.
Alternatives considered: Follow the HLD order and gate at the end of Phase 1 (rejected — inverts risk ordering; the cheapest week to learn the transport is wrong is week one). Trust ADR-1's existing rationale and skip measurement (rejected — ADR-1's own escape hatch presupposes a benchmark that had never been run). Defer until a Windows machine is available (rejected — see D-4, the decisive result did not need Windows).
Consequences: `oabench` exists as its own Go module and is expected to graduate to `cmd/oabench` in the v2 tree. It produced D-4, which materially changes the design before any of it was built. Its rootless netem lab (`netem/lab.sh`) is reusable as the HLD section 5 CI harness.

## D-4: One QUIC connection per peer does not carry bulk transfer on lossy paths
Date: 2026-07-25
Status: superseded by D-6 and D-12 — the measurements below stand and remain the evidence base for both; only the proposed decision is superseded, split into D-6 (control plane, accepted) and D-12 (bulk path, still open)
Context: HLD principle #1 is "one connection per peer" — all capabilities multiplex over a single QUIC connection, which is what makes "works over NAT, always" structural. v1.0's entire performance thesis is the opposite shape: N independent TCP connections, because networks throttle per-flow rather than in aggregate. D-3's harness measured whether that thesis survives being remapped onto N streams inside one QUIC connection.

Measurements: 192 MiB, median of 2 runs, chunk 1 MiB, single machine.

**All network conditions are emulated**, not measured over real hardware. `netem/lab.sh` applies `tc netem` (rate limit, fixed delay, random loss) to an isolated loopback inside an unprivileged network namespace containing no interface but `lo` and no route off it. No physical NIC, WiFi radio, hotspot or WAN link was involved in any row below. The profile names say which real-world link each preset is *modelling*:

|  | `lan-1g` | `hotspot` | `wifi-5g` | `wan-relay` |
|---|---|---|---|---|
| models this link | wired gigabit Ethernet | phone tethering / device-to-device AP | 5 GHz WiFi via home AP | VPS relay, both peers behind NAT |
| rate | 1 Gb/s | 600 Mb/s | 200 Mb/s | 100 Mb/s |
| RTT | 1 ms | 4 ms | 6 ms | 80 ms |
| loss | 0% | 0.05% | 0.1% | 0.5% |
| TCP best | 952 | 568 | 188 | 27.4 |
| QUIC GSO on | 660 | 559 | 185 | 5.5 |
| QUIC GSO off | 446 | 179 | 93 | 3.3 |

Throughput in Mb/s. Notes on each profile:

- `lan-1g` — wired gigabit Ethernet between two desktops.
- `hotspot` — phone tethering / device-to-device AP, no infrastructure hop; v1.0's 477 Mb/s case.
- `wifi-5g` — 5 GHz WiFi through a home access point; v1.0's 148 Mb/s case.
- `wan-relay` — cross-network hop via a VPS relay, both peers behind NAT (PRD R7/R8).

A fifth profile, `cgnat-punch` (50 Mb/s / 120 ms / 1%, modelling mobile data behind carrier-grade NAT), is defined in `netem/lab.sh` but was not run for this entry.

Emulation fidelity: netem is a token bucket plus fixed delay and uniform random loss. Real WiFi has contention, rate adaptation and bursty loss; a real relay has jitter and competing traffic. Absolute figures are therefore indicative of the modelled link, not predictive of it. What the emulation does support is the *comparison*: TCP and QUIC ran back-to-back under byte-identical conditions with the same framing and chunk size, so any inaccuracy in the model applies equally to both.

Stream scaling on wan-relay, 1/2/4/8 connections or streams:
- TCP: 3.75 / 6.97 / 14.6 / 27.4 Mb/s — near-linear, N independent congestion controllers.
- QUIC: 5.50 / 5.57 / 5.21 / 5.62 Mb/s — **flat**. One connection is one congestion controller; extra streams add no congestion window.

Note QUIC *wins* single-flow (5.5 vs 3.75) — its loss recovery is better per flow. It loses only because it cannot be parallelised the way v1.0 parallelises TCP.

Mechanism: quic-go v0.61 ships only Cubic, in `internal/congestion` with no exported knob and no BBR. Cubic is loss-based, so at 0.5% loss a single flow is pinned near the Mathis limit (~MSS/(RTT·√p)). TCP escapes that ceiling by running N flows; a single QUIC connection cannot.

This is the one result not confounded by the single-machine lab. The gigabit rows are CPU-bound — QUIC costs 15–25 CPU-s/GiB against TCP's 0.3–1.7, and sender, receiver and netem share one core — so `lan-1g` understates QUIC. But `wan-relay` runs at 3–27 Mb/s, nowhere near CPU-bound, and the flat stream-scaling curve is a property of the congestion controller, not of the test rig.

Decision (proposed): Keep one QUIC connection per peer for control, clipboard, notifications, input and remotefs metadata — the multiplexing and NAT benefits are real and the interactive traffic is not throughput-bound. Do **not** assume it carries bulk file transfer or media range-streaming on relayed or lossy paths. Pick one of:
  (a) Multiple QUIC connections for bulk transfer, restoring N congestion controllers. Costs principle #1 for the `files` capability and needs per-connection path setup.
  (b) Fork or vendor a BBR congestion controller into quic-go. BBR does not collapse on non-congestive loss and would likely close most of the gap on one connection. Ongoing maintenance cost against an `internal/` package.
  (c) PRD K1's own escape hatch: parallel-TCP bulk mode reusing v1.0's engine, QUIC for control.
Leaning (b) then (a): BBR preserves the architecture if it works, and is testable with a day of work. This entry stays `proposed` until that test runs.

Alternatives considered: Accept degraded relayed throughput (rejected — PRD R8 requires capability parity across paths and M4 wants seek <3 s on a relayed connection; 5 Mb/s does not stream video). Abandon QUIC for TCP (rejected — QUIC wins single-flow, and streams/datagrams/migration are what make PRD R9 and the mirror capability feasible).

Consequences: ADR-1 stays accepted for the session/control plane but its bulk-transfer assumption is unproven and must be resolved before the `files` capability is written. HLD principle #1 needs a stated exception for bulk. Two secondary findings recorded: (1) K1 is real — disabling GSO costs 50–70% of QUIC throughput and triples CPU on every profile, so the Windows send path needs a real measurement, though it is no longer the *first* problem; (2) QUIC's 10–20× CPU cost per byte is a live risk for PRD R30 (Android battery, <50 MB RSS desktop daemon) and feeds ADR-5.

## D-6: ADR-1 — Transport is QUIC for the session and control plane, not (yet) for bulk
Date: 2026-07-26
Status: accepted
Context: HLD section 6 lists ADR-1 as already accepted, with "parallel-stream bulk mode" as an escape hatch should the benchmark gate fail off-Linux. D-3 built that benchmark and D-4 ran it. The result splits the question: QUIC is sound for everything that is not throughput-bound, and unproven for everything that is. Accepting or rejecting ADR-1 wholesale would misstate the evidence in one direction or the other.
Decision: Accept QUIC (quic-go) as the transport for the session layer, capability negotiation and multiplexing, and the control, clipboard, notification, input and remotefs-metadata capabilities. HLD principle #1 — one connection per peer — holds for all of these. The bulk data path (`files`, remotefs range streaming) is explicitly outside this ADR and is decided in D-12.
Alternatives considered: TCP for everything (rejected — on the emulated relayed profile QUIC beat TCP per flow, 5.5 vs 3.75 Mb/s, and QUIC's streams, datagrams and connection-ID migration are what make PRD R9 live path migration and the `mirror` capability feasible at all; TCP would force a separate mechanism for each). Withhold acceptance until a real Windows measurement exists (rejected — K1 is a throughput risk, and control-plane traffic is not throughput-bound; blocking the session layer on it would stall Phase 1 for a risk that does not apply to it).
Consequences: Session, identity and capability work is unblocked and can proceed on quic-go. K1 still needs a real Windows run, but it now gates only the bulk path. QUIC's measured CPU cost (D-4: 15–25 CPU-s/GiB against TCP's 0.3–1.7) is immaterial at control-traffic volumes but is a live input to D-9 (media plane) and D-10 (Android battery, PRD R30).

## D-7: ADR-2 — Session crypto is TLS 1.3 with self-signed certificates pinned by Ed25519 device key
Date: 2026-07-26
Status: accepted
Context: HLD ADR-2 weighed TLS-1.3-with-pinned-certs against Noise_IK and required a decision "before transport code". D-3's spike implemented the TLS path end to end in `oabench/bench/tlsutil.go` and every benchmark run in D-4 was carried over it, so the option is no longer hypothetical.
Decision: Each device holds one long-term Ed25519 keypair. That same key is the TLS certificate key in a per-device self-signed certificate. Peers are verified in `VerifyPeerCertificate` by comparing the presented raw public key against the pinned one — no certificate authority, no chain building, no hostname validation. DeviceID is truncated SHA-256 of the public key, base32, matching HLD section 3.1.
Alternatives considered: Noise_IK (rejected — quic-go mandates TLS 1.3 as QUIC's own handshake and exposes no hook to substitute another key exchange. Adopting Noise would mean either a second encryption layer running inside QUIC streams, paying framing and CPU twice on a transport already costing 10–20x TCP per byte, or forking quic-go's handshake and owning it. Neither is justified, because pinned TLS 1.3 already delivers the property Noise_IK was wanted for: authentication by key possession rather than by any authority). CA-issued certificates (rejected — requires an issuing authority, contradicting PRD R1's "no accounts, no server-side identity"). A separate TLS keypair distinct from the device identity key (rejected — Go's crypto/tls signs with Ed25519 under TLS 1.3, so one key serves both roles; a second keypair would add a mapping to maintain and a second thing to revoke, for no gain).
Consequences: Transport and identity work are unblocked; this was the stated blocker in HLD section 6. Pinning makes key rotation a protocol concern — a device whose key changes hard-fails until re-paired, which PRD R2 already specifies as intended. Relays and rendezvous observe ciphertext and routing metadata only, satisfying R27. The spike's fixed Ed25519 seed exists solely so the benchmark client can pin the server without an out-of-band exchange and must not reach the daemon; production keys are generated per install and persisted in the trust store.

## D-8: ADR-3 — Owned-level access requires a local unlock to start a session
Date: 2026-07-26
Status: proposed — needs maintainer sign-off; no measurement will settle this one
Context: PRD K10 and R3. Unattended "Owned" access is the feature that makes S3 (working from a hostel network against a machine nobody is sitting at) possible, and it is also the feature that makes a stolen paired laptop equivalent to owning every other machine. The open question was whether to require a second factor to *use* Owned access or to accept SSH-like semantics where possession of the key is sufficient.
Decision (proposed): Require a device-local unlock (OS biometric or PIN) to *initiate* an Owned-level session. Configurable per device, default on. Do not require re-authentication per operation within a live session.
Rationale: the threat being defended against is an unattended or stolen device, which a session-initiation gate covers. Gating each operation instead would defend against nothing extra in that scenario while making the away-from-home working session unusable.
Alternatives considered: SSH-like, key possession alone (rejected as the default — SSH keys in practice are protected by a passphrase plus an agent that caches it, which is structurally the same design as proposed here; adopting SSH's model minus SSH's passphrase habit would be strictly weaker than the comparison implies). Per-operation unlock (rejected — destroys S3, the scenario unattended access exists to serve). No second factor at all, documented as accepted risk (rejected — R3 already makes promotion to Owned a deliberate act; leaving the resulting capability entirely unguarded is inconsistent with that care).
Consequences: Requires a local-authentication adapter in the per-platform shells on all three OSes, joining `Clipboard`, `Notifier`, `Capturer` and `Injector`. Adds a field to the trust-store record, which is why this must be settled before the trust store schema is written — retrofitting a schema change across already-paired devices is a migration worth avoiding. This is a product tradeoff rather than an engineering one, so it is flagged for explicit sign-off rather than resolved by evidence.

## D-9: ADR-4 — Media plane decision deferred until the bulk path is settled
Date: 2026-07-26
Status: proposed — deferred, blocked on D-12
Context: HLD ADR-4 leans toward QUIC datagrams for the `mirror` capability, with a Moonlight-style raw RTP-over-UDP sidecar as the fallback if datagrams cannot hold latency. D-3's spike measured streams only; datagrams were not exercised, so no direct evidence exists yet.
Decision (proposed): Still try datagrams first, but decide this only after D-12, because two findings from D-4 change the inputs. First, QUIC's CPU cost of 15–25 CPU-s/GiB is comfortable at mirror bitrates on a desktop but is an open question on Android at high bitrate, feeding D-10. Second, and more structurally: RFC 9221 datagrams are congestion-controlled by the connection they ride on, so on a single QUIC connection the mirror stream shares one congestion controller with everything else — including bulk file transfer. That is the same single-controller property that sank bulk throughput in D-4. If D-12 moves bulk off this connection, the contention disappears and datagrams look considerably better; if it does not, `mirror` and `files` compete for one congestion window and HLD's priority classes have to carry the entire burden of keeping latency bounded.
Alternatives considered: Commit to the raw RTP/UDP sidecar now (rejected — it introduces a second NAT-traversal surface and a second crypto surface to audit, which is precisely what one-connection-per-peer exists to avoid; the datagram path has not been shown to fail, only shown to be coupled to an unresolved decision).
Consequences: Needs its own spike once D-12 lands, measuring datagram goodput and latency under loss, and specifically latency while a bulk transfer shares the same connection. `oabench` already has the shaped lab; it needs a datagram mode and a latency histogram.

## D-10: ADR-5 — Android core is the gomobile-bound Go core
Date: 2026-07-26
Status: proposed
Context: HLD ADR-5 weighs a gomobile-bound Go core against reimplementing the protocol in Kotlin. PRD G1 requires Android to be first-class from Phase 1, so this cannot be deferred past Phase 1 planning.
Evidence from D-3: `CGO_ENABLED=0 GOOS=android GOARCH=arm64 go build` succeeds for quic-go and the whole benchmark module, so the transport is portable to Android as pure Go and the "does it even compile for Android" risk is retired. Not answered, because no Android NDK was available on the development machine: gomobile binding friction, APK size delta, on-device throughput, and battery cost.
Decision (proposed): gomobile-bound Go core, one protocol implementation shared across all platforms.
Alternatives considered: Kotlin reimplementation of the protocol (rejected pending contrary evidence — it doubles the implementation surface of a security-critical wire protocol and doubles the audit burden for end-to-end crypto, and PRD R32's mixed-version capability negotiation is materially harder to keep correct across two independent codebases than across one). Defer Android to Phase 2 (rejected — directly contradicts PRD G1 and the Phase 1 exit criteria).
Consequences: A follow-up spike must install the NDK and measure `gomobile bind`, APK size delta, and on-device throughput before this moves to accepted. D-4's CPU finding is a direct risk to PRD R30's Android battery budget and must be measured on a real mid-range device rather than a Pixel, per PRD K5. If binding friction or APK size proves prohibitive, the fallback is not Kotlin reimplementation but running the Go core in a separate process and speaking the local IPC protocol the desktop shells already use.

## D-11: ADR-6 — No consensus or replication layer
Date: 2026-07-26
Status: accepted
Context: An earlier 2.0 protocol draft carried a consensus mechanism (STPB) and manifest replication, designed for multi-device coordination. HLD section 6 records these as explicitly dropped for the current vision; this entry makes that a logged decision rather than a note in a design document.
Decision: No consensus protocol and no manifest replication in v2. Every capability is a pairwise session between exactly two devices, and pairwise sessions require no agreement protocol.
Alternatives considered: Retain a lightweight replication layer for possible future N>2 features (rejected — speculative. PRD NG2 rules out multi-user sharing entirely, and the only plausible N>2 case is a single clipboard shared across N devices with conflict resolution, which is not in scope. Building an agreement layer against a hypothetical is the kind of cost that never gets removed once present).
Consequences: Revisit only if a capability genuinely requires agreement among three or more devices. Worth noting for future readers: chunk manifests still exist in the `files` capability for resume-after-interruption (PRD R13), and that is per-transfer bookkeeping, not replication — the name collision with the dropped "manifest replication" should not be read as resume having been dropped too.

## D-12: ADR-7 — Bulk transport path is undecided; the BBR experiment is the gate
Date: 2026-07-26
Status: proposed — blocks the `files` capability and D-9
Context: The HLD lists no ADR for the bulk data path, because it assumed one QUIC connection carries every capability. D-4 measured that assumption and it does not hold on lossy paths: on the emulated relayed profile, TCP scales 3.75 to 27.4 Mb/s across one to eight connections while QUIC stays flat at roughly 5.5 Mb/s across one to eight streams. quic-go v0.61 ships only Cubic, in `internal/congestion`, with no BBR and no exported knob, so a single connection is a single loss-based flow pinned near the Mathis limit. v1.0's entire performance thesis was N independent congestion controllers, and streams inside one connection do not reproduce it. The relayed path is exactly where this matters most, since PRD R8 requires capability parity across paths and the relay is the always-available fallback.
Decision (proposed): None yet. Options, in the order they should be *tested* — which is not the order of preference, because the cheapest experiment is not the preferred outcome:
  (a) Multiple QUIC connections for bulk transfer, restoring N congestion controllers. **Test this first**, not because it is preferred but because it is nearly free: `oabench` already dials one connection with N streams, and dialling N connections with one stream each is a small change plus a rerun of the `wan-relay` profile. It bounds the problem — it establishes the ceiling any single-connection solution would have to reach, and yields a working fallback either way. Costs principle #1 for the `files` capability specifically, and multiplies path setup, hole punching and migration work by N.
  (b) Vendor or fork a BBR congestion controller into quic-go. BBR probes for bandwidth and delay rather than treating loss as congestion, so it does not collapse at 0.5% loss the way Cubic does, and it is the only option that leaves principle #1 intact. **Correction to an earlier draft of this entry, which called this cheap to test: it is not.** Verified 2026-07-26: neither quic-go v0.61 nor the apernet fork ships BBR. `internal/congestion` defines a `SendAlgorithm` interface, so the seam exists, but it is unexported and there is no injection point — using BBR means vendoring quic-go and porting an implementation (hysteria's Go BBR is the obvious candidate), then carrying that patch across upstream releases. Days of work plus validation, not a day. Worth it only if (a) confirms the ceiling is high enough to be worth chasing.
  (c) PRD K1's own escape hatch: parallel-TCP bulk mode reusing v1.0's engine with QUIC for control. Known to work, since it is v1.0, but it means two transports, two NAT stories, and TCP hole punching is materially harder than UDP.
Alternatives considered: Accept degraded throughput on relayed paths (rejected — PRD R8 requires parity across paths and M4 targets seek under 3 s over a relayed connection; 5.5 Mb/s does not stream video). Abandon QUIC entirely (rejected — see D-6; QUIC wins per flow and carries the control plane well).
Consequences: Blocks the `files` capability and couples to D-9, since whether bulk shares the media connection changes the datagram calculus. Whichever option wins, HLD principle #1 needs an explicitly stated exception or an explicit reaffirmation. The deciding experiment is a congestion-control swap plus a rerun of the `wan-relay` profile in `oabench`.

## D-13: Multiplexing helps neither transport on clean links; QUIC degrades past two streams; GSO is Linux-only by construction
Date: 2026-07-26
Status: accepted (evidence entry; extends D-4, changes no decision)
Context: D-4 reported one "best" throughput figure per profile and broke out stream scaling only for `wan-relay`. That collapsed away the comparison the whole spike was built to make — multiplexed QUIC with GSO enabled against v1.0's multiplexed TCP, at matched stream counts, on every profile. The data existed in `oabench/netem/results-2026-07-25.jsonl` from the original run and was simply not surfaced. This entry publishes it, and adds one finding obtained by reading quic-go rather than by measurement.

Full matrix, Mb/s, by stream/connection count. Same run as D-4 (192 MiB, median of 2, 1 MiB chunks, single machine, emulated conditions per D-4's fidelity note):

| profile | config | 1 | 2 | 4 | 8 |
|---|---|---|---|---|---|
| `lan-1g` | TCP | 933.8 | 950.2 | 952.8 | 952.3 |
| | QUIC GSO on | 645.2 | 660.6 | 626.2 | 507.0 |
| | QUIC GSO off | 303.3 | 419.7 | 446.8 | 424.0 |
| `hotspot` | TCP | 554.6 | 562.7 | 565.0 | 568.3 |
| | QUIC GSO on | 505.2 | 559.8 | 537.8 | 446.7 |
| | QUIC GSO off | 159.4 | 179.2 | 178.3 | 163.7 |
| `wifi-5g` | TCP | 188.7 | 188.0 | 188.1 | 187.2 |
| | QUIC GSO on | 165.9 | 185.4 | 175.3 | 180.0 |
| | QUIC GSO off | 91.1 | 92.3 | 91.7 | 93.9 |
| `wan-relay` | TCP | 3.7 | 6.9 | 14.6 | 27.3 |
| | QUIC GSO on | 5.5 | 5.5 | 5.2 | 5.6 |
| | QUIC GSO off | 3.3 | 3.2 | 3.3 | 3.3 |

Findings:

1. **On clean links, multiplexing buys nothing — for either transport.** A single TCP connection already saturates every lossless profile: 933.8 of 952.3 on `lan-1g`, 554.6 of 568.3 on `hotspot`, 188.7 on `wifi-5g` where more connections make it marginally *worse*. v1.0's parallel-connection trick is not a general throughput mechanism; it is specifically a loss-and-latency mechanism. This reframes v1.0's headline result: the 5–10x it reports came from links where a single flow was being held back, not from parallelism being inherently faster.

2. **Multiplexing never helps QUIC, and hurts it past two streams.** GSO-on peaks at two streams on every profile and then declines — `lan-1g` 660.6 down to 507.0, `hotspot` 559.8 down to 446.7. Extra streams add scheduling and per-stream bookkeeping against one congestion window and one sender loop, so they cost without buying. Practical consequence: if QUIC carries bulk, the stream count should be about two, not the eight v1.0 uses. D-12's option (a) must therefore be read as multiple *connections*, not more streams — more streams is already measured and does not work.

3. **TCP's advantage exists only under loss, and only through parallelism.** `wan-relay` is the sole profile where the transports diverge on scaling: TCP 3.7 to 27.3 near-linearly, QUIC flat at ~5.5. Per single flow QUIC wins there (5.5 against 3.7). Restated: QUIC is the better protocol per flow and loses solely because it cannot be run N-up inside one connection.

4. **GSO is Linux-only by construction, so the "GSO off" row is what Windows and macOS get — permanently.** Established by reading quic-go v0.61, not by measurement: `isGSOEnabled` returns a hardcoded `false` in `sys_conn_helper_darwin.go` and `sys_conn_helper_freebsd.go`, and `appendUDPSegmentSizeMsg` is a no-op stub in `sys_conn_helper_nonlinux.go`, which is what Windows compiles against. Only `sys_conn_helper_linux.go` implements `UDP_SEGMENT`. This upgrades PRD risk K1 from "a risk to be measured on a Windows machine" to a property of the library: on Windows and macOS, QUIC bulk throughput is the GSO-off row — 93 of TCP's 188 on `wifi-5g`, 179 of 568 on `hotspot`, 446 of 952 on `lan-1g`.

Consequences: No decision changes. D-12's framing is refined — its option (a) is multiple connections, since additional streams are now measured as counterproductive, and its BBR option (b) addresses finding 3 but not finding 4, which no congestion controller can fix. Finding 4 is a direct problem for PRD G1's "Windows and Linux and Android are all first-class, with parity as the bar", and should be weighed in D-12 alongside throughput: options (a) and (b) both leave Windows on the degraded send path, whereas option (c), parallel TCP for bulk, is the only one that does not. The clean-link QUIC shortfall in this table is still subject to D-4's co-location caveat and may narrow on two real machines; findings 1, 2 and 4 do not depend on that caveat.

# Decision Tree

How to read this file. The trees below are the **index**: they show which questions were asked, which branches were explored, and which one was taken. The full entries beneath them are the **record**: why each branch was rejected, what was measured, and what each choice costs. A diagram cannot hold rationale, so both exist and the entries remain the normative text.

Append-only. See `AGENTS.md` for format and rules. Do not delete or rewrite past entries — supersede them.

Legend: **green** = accepted · **orange** = open or needs a decision · **red** = considered and rejected · **grey** = evidence · **purple** = superseded.

## Status at a glance

| Entry | ADR | Question | Status |
|---|---|---|---|
| D-1 | — | Where do decisions and code knowledge live? | accepted |
| D-2 | — | Which modules does the project keep? | accepted |
| D-3 | — | When is the transport gate run? | accepted |
| D-4 | — | Does v1.0's parallelism survive on QUIC streams? | evidence · superseded by D-6, D-12 |
| D-5 | — | *(withdrawn — number not reused)* | — |
| D-6 | ADR-1 | What transport carries the session? | **accepted** — QUIC, control plane |
| D-7 | ADR-2 | What secures the session? | **accepted** — TLS 1.3, pinned Ed25519 |
| D-8 | ADR-3 | Second factor for unattended Owned access? | superseded by D-18 |
| D-9 | ADR-4 | What carries the media plane? | open, constrained by D-14 |
| D-10 | ADR-5 | How does Android run the core? | proposed — gomobile |
| D-11 | ADR-6 | Keep consensus/replication? | accepted — dropped |
| D-12 | ADR-7 | What carries bulk transfer? | superseded by D-14 |
| D-13 | — | Full stream-count matrix | evidence |
| D-14 | ADR-7 | What carries bulk transfer? | **accepted** — vendor quic-go + BBR |
| D-15 | — | How is this file organised? | accepted |
| D-16 | ADR-7 sub | BBRv1 or BBRv2/v3? | **accepted** — v1, on availability |
| D-17 | — | What does sharing a connection cost interactive latency? | evidence — less than separate connections |
| D-18 | ADR-3 | Second factor for unattended Owned access? | **accepted** — gate + 6h token |
| D-19 | ADR-3 | How is the device key protected at rest? | accepted — items resolved by D-20, D-21 |
| D-20 | ADR-3 | Can a gated key stay reachable unattended? | **accepted** — split identity/privilege keys |
| D-21 | ADR-3 | What if a device cannot protect its key? | **accepted** — three protection tiers |
| D-22 | ADR-8 | How is the Windows send-path gap closed? | **accepted** — implement USO in the fork |
| D-23 | ADR-8 | Is the receive side separate work? | **accepted** — no, one Windows fast path |
| D-24 | — | How are priority classes enforced? | **accepted** — session-layer bulk quiesce |
| D-25 | — | Consent, session lifecycle, expiry-with-work-in-flight? | **accepted** — hybrid consent, capped grace |
| D-26 | — | Where does v2 code live? | **accepted** — one root module |
| D-27 | — | What does "the fork" mean mechanically? | **accepted** — fork repo + `replace` |
| D-28 | — | Protobuf toolchain? | **accepted** — `buf`, codegen committed |
| D-29 | — | Daemon-to-UI IPC? | **accepted** — reuse the session envelope |
| D-30 | ADR-3 | Is the unlock token per peer or global? | **accepted** — per peer |
| D-31 | ADR-5 | Does gomobile actually bind quic-go? | **accepted** — yes, 8.4 MB/ABI |
| D-32 | ADR-8 | When is the Windows work done? | **accepted** — deferred to Phase 2 |
| D-33 | ADR-8 | What does Windows actually cost? | evidence — 1450 Mb/s at 1 stream, 647 at 4 |
| D-34 | — | Are the wire schemas implementable? | **accepted** — 11 protos; found 6 spec defects · corrected on enums by D-39 |
| D-35 | ADR-7 sub | How is the BBR dependency actually obtained? | **accepted** — pseudo-version fork + vendored BBR · corrects D-16 |
| D-36 | ADR-7 sub | Which BBR gain profile is the default? | **accepted** — conservative, latency-first |
| D-37 | — | What authorises an inbound peer? | **accepted** — `Config.Authorize` callback |
| D-38 | — | Which error code closes an unknown envelope version? | **accepted** — `PROTOCOL_VIOLATION`; §3/§10 disagree |
| D-39 | — | Which enums are offset by one? | **accepted** — not `msgType`, not `ProtectionTier` |
| D-40 | — | What is a chunk offset relative to? | **accepted** — transfer-global, files concatenated |
| D-41 | — | How does the session dispatch to capabilities? | **accepted** — bounded queue per capability |
| D-42 | ADR-3 | Where is the privilege *public* key while sealed? | accepted — interim sidecar, pending Appendix A v2 |
| D-43 | — | Is there a threat model? | accepted — document done; 4 questions open |
| D-44 | — | What does CI enforce? | **accepted** — build/vet/test/Windows/buf; netem manual |
| D-45 | — | Is §5.2's SAS transcript fully specified? | **accepted** — three gaps filled; §5.2 and §6.4 need edits |
| D-46 | — | How does a last message survive the close behind it? | **accepted** — 250 ms linger before close |
| D-47 | — | What is the unicast fallback's byte layout? | **accepted** — defined here; §15.2 specifies none |
| D-48 | — | May a process browse without announcing? | **accepted** — yes, `BrowseOnly` |
| D-49 | ADR-5 | How does the Android shell reach the core? | **accepted** — `mobile/` façade, .aar not in VCS |
| D-50 | ADR-8 | Is ADR-8 implemented, and on what base? | **written, unpublished, unmeasured** · corrects D-22, D-27 |
| D-51 | — | What carries daemon IPC, concretely? | **accepted** — capID 7, `request_id` field 1 everywhere |
| D-52 | — | Where does an inbound Hello run? | **accepted** — off the accept path; a refusal is not fatal |
| D-53 | — | Who answers for an unattended daemon? | **accepted** — nobody watching means refused |
| D-54 | — | How does the core reach a system clipboard? | **accepted** — the desktop's own helper, as a subprocess |
| D-55 | — | What does a refused peer hear? | **accepted** — NOT_PAIRED, and the dialler translates it |
| D-56 | — | What is Android's daemon? | **accepted** — a foreground service, prompts via notification |
| D-57 | — | How does an AuthProof reach the operation it authorises? | **accepted** — it precedes the message and is spent by it |
| D-58 | — | Who enforces §6.5's one-hour grace? | **accepted** — the initiator; the verifier cannot see an expiry |
| D-59 | — | Where does the unlock credential come from? | **accepted** — the shell, over the local socket; the daemon never prompts |
| D-60 | — | Is the protection tier configured or detected? | **accepted** — read off the disk; `openair protect` creates it |
| D-61 | — | Which tiers does M6 actually ship? | **accepted** — keystore on Android, passphrase on desktop; Linux TPM deferred |

**Open right now:** D-9 (media plane, Phase 4). Linux tier 1 — TPM-sealed privilege keys — is deferred by D-61, which leaves the maintainer's own machine on tier 2 despite having a TPM. D-10 is settled by D-31. Awaiting hardware: on-device Android throughput and battery (runbook in `oabench/androidkit/`). The Windows baseline is deferred to Phase 2 by D-32, though it remains a hard Phase 1 *exit* blocker. One optional refinement is flagged for the maintainer in D-30 — ephemeral per-peer delegation keys, which would make per-peer scope cryptographic rather than policy-enforced. Every decision gating Phase 1 is made and the trust store schema is fully determined. ADR-3 is fully resolved across D-18, D-19, D-20 and D-21, so the trust store schema is unblocked. D-16's queueing worry is answered by D-17; the port has now landed (D-35, D-36), so what remains is comparing BBRv1 against D-17's Cubic baseline.

**Raised by wave 1, needing the maintainer rather than an agent.** Four security questions from the threat model (D-43): the D-20/D-21 conflict over whether a TPM-sealed key survives cold theft, the trust store's unspecified at-rest integrity, `RelayAuth`'s missing domain separation, and DeviceID as a permanent tracking identifier. Separately, `PROTOCOL.md` needs four edits that wave 1 found and worked around rather than fixed: §8.3's chunk-offset semantics (D-40, the urgent one — it is wire-visible and two implementations choosing differently will corrupt files), §3 versus §10 on unknown versions (D-38), §3's claim that every enum is offset (D-39), and Appendix A gaining a public-key field (D-42). Two interface gaps are also outstanding: `identity.Identity` does not declare `ProtectionTier()` though Hello carries one, and `session.Handler` has no version accessor, which leaves §4's per-capability version negotiation degenerate.

## Transport — the deep branch

```mermaid
flowchart TD
    Q1{"ADR-1<br/>What transport carries v2?"}:::question
    Q1 --> D6["D-6 · QUIC<br/>session and control plane"]:::accepted
    Q1 -.rejected.-> R1["TCP everywhere<br/>no connection migration,<br/>no datagrams, N TLS handshakes,<br/>TCP hole punching is harder"]:::rejected

    D6 --> E1["D-4 · D-13 evidence<br/>one QUIC connection flat at 5.5 Mb/s<br/>across 1-8 streams, vs parallel TCP<br/>scaling 3.7 to 27.3 on wan-relay"]:::evidence
    E1 --> Q2{"ADR-7<br/>What carries bulk transfer?"}:::question

    Q2 -.rejected.-> R2A["a · N QUIC connections<br/>costs the one-connection principle<br/>for files, multiplies path setup<br/>and migration by N"]:::rejected
    Q2 ==chosen==> D14["D-14 · b · vendor quic-go, add BBR<br/>one-connection principle holds for bulk;<br/>BBR paces to bandwidth and RTprop<br/>instead of treating loss as congestion"]:::accepted
    Q2 -.rejected.-> R2C["c · parallel-TCP bulk mode<br/>two transports, two NAT stories"]:::rejected

    D14 ==chosen==> SUB["D-16 · BBRv1 from hysteria<br/>chosen on availability, not merit:<br/>no BBRv2/v3 exists in Go at all"]:::accepted
    SUB -.rejected.-> R3V["port BBRv3 from QUICHE<br/>research-grade port, and its ~2% loss<br/>threshold sits above both our profiles<br/>so the goodput gain would be near zero"]:::rejected
    SUB --> QUEUE["D-17 · measured, and it reverses the worry<br/>shared QUIC ping 12.2 ms p50 under load<br/>vs 83.7 ms on a separate TCP connection.<br/>Cubic baseline BBRv1 must beat: 12.2 ms"]:::evidence
    D14 --> COST["Cost, revised by D-16: use apernet/quic-go,<br/>which exports a congestion API, plus<br/>hysteria BBR. Burden becomes tracking<br/>their fork, not carrying a local patch"]:::evidence
    D14 --> GSO["Send-path gap · Windows only<br/>UDP_SEGMENT is Linux-only by construction.<br/>Android compiles the linux tag so it is fine;<br/>macOS is best-effort per G1"]:::evidence
    R2C -.->|"the only option that<br/>would have avoided this"| GSO
    GSO ==chosen==> D22["D-22 · ADR-8 · implement USO in the fork<br/>UDP_SEND_MSG_SIZE, sticky socket option<br/>rather than a per-send cmsg. gsoSize already<br/>reaches the platform layer and is discarded"]:::accepted
    D22 -.deferred by.-> D32["D-32 · Windows work moves to Phase 2<br/>a performance patch, not architecture, so it<br/>does not gate the LLD. Windows cross-compiled<br/>in CI meanwhile so the platform cannot rot"]:::open
    D32 --> D33["D-33 · measured: 1450 Mb/s at 1 stream,<br/>2.1x better than the GSO-off proxy predicted,<br/>but 647 at 4 streams. Use 1-2 streams for bulk;<br/>ADR-8 is not urgent for throughput"]:::evidence
    D22 ==extended by==> D23["D-23 · one Windows fast path, both directions<br/>Windows falls back to basicConn: no batching,<br/>and WritePacket panics on gsoSize. USO and URO<br/>share that prerequisite, so they ship together"]:::accepted

    classDef question fill:#1565c0,color:#fff,stroke:#0d47a1
    classDef accepted fill:#2e7d32,color:#fff,stroke:#1b5e20
    classDef rejected fill:#b71c1c,color:#fff,stroke:#7f0000
    classDef open fill:#ef6c00,color:#fff,stroke:#e65100
    classDef evidence fill:#455a64,color:#fff,stroke:#263238
```

## Security and identity

```mermaid
flowchart TD
    Q3{"ADR-2<br/>What secures the session?"}:::question
    Q3 ==chosen==> D7["D-7 · TLS 1.3<br/>self-signed cert keyed by the<br/>Ed25519 device identity,<br/>peer pinned by raw public key,<br/>no CA anywhere"]:::accepted
    Q3 -.rejected.-> R3A["Noise_IK<br/>quic-go mandates TLS 1.3 as QUIC's<br/>own handshake with no hook to replace it;<br/>would mean a second encryption layer<br/>inside streams, or forking the handshake"]:::rejected
    Q3 -.rejected.-> R3B["CA-issued certificates<br/>needs an issuing authority,<br/>contradicts PRD R1"]:::rejected
    Q3 -.rejected.-> R3C["separate TLS keypair<br/>a second key to map and revoke,<br/>for no gain"]:::rejected

    Q4{"ADR-3<br/>Second factor for<br/>unattended Owned access?"}:::question
    Q4 ==chosen==> D18["D-18 · biometric or passcode to start<br/>a session, then a 6-hour token;<br/>manual end or expiry forces re-auth;<br/>opt-in never-expire per device"]:::accepted
    D18 --> D30["D-30 · scope is per peer<br/>one prompt per device per 6 hours,<br/>so the prompt can name what it grants"]:::accepted
    D18 ==answered by==> D19["D-19 · key encrypted at rest under K_master<br/>keystore unseal, or Argon2id from a PIN;<br/>both decrypt the Ed25519 key into RAM<br/>for the 6-hour window"]:::accepted
    D19 ==resolved by==> D20["D-20 · two keys per device<br/>identity key always warm, keeps the machine<br/>reachable and runs clipboard and notifications;<br/>privilege key gated, needed only for Owned ops.<br/>D-18's never-expire IS the always-on designation"]:::accepted
    D19 ==resolved by==> D21["D-21 · three protection tiers<br/>1 keystore or TPM · 2 passphrase via Argon2id<br/>3 neither, so Trusted only, no Owned.<br/>Maintainer's Fedora box has TPM 2.0, so tier 1"]:::accepted
    D7 ==pinned by==> D45["D-45 · pairing transcript, §5.2 gaps filled<br/>keys sorted by value so the digits are<br/>role-independent; absent privilege key encoded<br/>as 32 zero bytes; nonces ordered by §5.1 role"]:::accepted
    D45 --> D46["D-46 · a message that ends the conversation<br/>lingers 250 ms before the close;<br/>CONNECTION_CLOSE overtakes unflushed stream data,<br/>losing a declined PairConfirm or an unpair"]:::accepted

    D30 ==implemented by==> D57["D-57 · an AuthProof precedes the message<br/>it authorises and is spent by it;<br/>the schema gives the proof no capID or msgType,<br/>so it can only be verified against a request"]:::accepted
    D57 --> D58["D-58 · §6.5's one-hour grace is enforced<br/>on the initiator; the wire carries no expiry,<br/>so the verifier cannot compute the cap.<br/>Its protection is that no new operation starts"]:::accepted
    D19 ==implemented by==> D59["D-59 · the shell holds the credential and<br/>sends it over the local socket;<br/>a daemon has no terminal and must not<br/>be able to obtain one without a user"]:::accepted
    D21 ==implemented by==> D60["D-60 · the protection tier is read off disk,<br/>not configured; a flag would let a typo<br/>drop a device out of Owned silently"]:::accepted
    D21 ==shipped as==> D61["D-61 · tier 1 on Android via Keystore-wrapped KEK,<br/>tier 2 on desktop via passphrase.<br/>Linux TPM sealing deferred: two PCR policies,<br/>a new dependency, and hardware-dependent tests"]:::accepted

    Q4 -.rejected.-> R4A["SSH-like, key possession alone<br/>SSH keys carry a passphrase in practice;<br/>adopting the model without the habit<br/>is strictly weaker"]:::rejected
    Q4 -.rejected.-> R4B["unlock per operation<br/>destroys S3, the away-from-home<br/>session it exists to serve"]:::rejected

    classDef question fill:#1565c0,color:#fff,stroke:#0d47a1
    classDef accepted fill:#2e7d32,color:#fff,stroke:#1b5e20
    classDef rejected fill:#b71c1c,color:#fff,stroke:#7f0000
    classDef open fill:#ef6c00,color:#fff,stroke:#e65100
```

## Platform and capabilities

```mermaid
flowchart TD
    Q5{"ADR-4<br/>What carries the media plane?"}:::question
    Q5 --> D9["D-9 · still open, lean now C<br/>D-24 quiesces bulk, which removes the<br/>scheduling argument for datagrams and makes<br/>stream-per-frame with RESET_STREAM favourite"]:::open
    D9 -.-> D24N["D-24 · quic-go has NO stream priority API.<br/>HLD 3.4's enforcement mechanism does not exist.<br/>Solved above the transport instead of<br/>a third patch to the fork"]:::evidence
    Q5 -.deferred.-> R5A["raw RTP/UDP sidecar<br/>a second NAT and crypto surface;<br/>kept as the fallback, not rejected"]:::rejected

    Q6{"ADR-5<br/>How does Android run the core?"}:::question
    Q6 ==chosen==> D10["D-31 · gomobile-bound Go core, verified<br/>binds quic-go in 24 s, 8.4 MB per ABI,<br/>clean Java API with Go errors as exceptions.<br/>On-device throughput and battery still open"]:::accepted
    Q6 -.rejected.-> R6A["Kotlin reimplementation<br/>doubles the surface of a<br/>security-critical wire protocol<br/>and its audit burden"]:::rejected

    Q8{"§15<br/>How are peers found on a LAN?"}:::question
    Q8 ==chosen==> D47["D-47 · mDNS `_openair._udp` (§15.1) plus a<br/>unicast UDP beacon (§15.2), whose byte layout<br/>the spec never defined and this does.<br/>Query plus announce, answered unicast,<br/>so discovery converges in one round trip"]:::accepted
    D47 --> D48["D-48 · a process with no listening port<br/>browses without announcing;<br/>announcing one anyway publishes an address<br/>that refuses every connection to it"]:::accepted
    D47 -.constrained by.-> D47N["a candidate is unauthenticated.<br/>Discovery never dials; the pinned-key<br/>handshake is what decides"]:::evidence

    Q9{"D-29 / M4<br/>How does a shell drive the daemon?"}:::question
    Q9 ==chosen==> D51["D-51 · the session envelope over a unix socket<br/>or named pipe, capID 7, local only.<br/>`request_id` is field 1 in every daemon message,<br/>which is what lets a reply be routed<br/>without knowing its type"]:::accepted
    D51 --> D53["D-53 · with no client able to answer and no<br/>--accept-all, an inbound transfer is refused.<br/>A daemon that accepted files because nobody<br/>was watching is the worse default"]:::accepted
    D51 -.rejected.-> R9A["gRPC, as HLD 2 first said<br/>a second codegen path and a large<br/>dependency for one local socket<br/>with a single trusted client"]:::rejected
    D51 -.constrained by.-> D51N["the socket is a trust boundary:<br/>0700 dir, 0600 socket, SO_PEERCRED on Linux,<br/>owner-only pipe ACL on Windows"]:::evidence

    Q10{"M4<br/>Where does an inbound Hello run?"}:::question
    Q10 ==chosen==> D52["D-52 · on its own goroutine, bounded at 32,<br/>with a 10 s deadline. A refused peer becomes a<br/>HandshakeError, which does not end the loop"]:::accepted
    Q10 -.rejected.-> R10A["inline on the accept path, as M1 had it<br/>one peer that connects and says nothing<br/>stops every other device from arriving"]:::rejected

    Q11{"§9 / M5<br/>How does the core reach a system clipboard?"}:::question
    Q11 ==chosen==> D54["D-54 · exec the desktop's own helper —<br/>wl-copy, xclip, xsel, pbcopy, Set-Clipboard.<br/>No display dependency in a daemon that<br/>mostly runs without one"]:::accepted
    Q11 -.rejected.-> R11A["cgo X11/Wayland binding<br/>a build dependency per platform,<br/>and the Windows cross-build gate<br/>would need it too"]:::rejected
    D54 -.constrained by.-> D54N["wl-copy forks and holds the selection.<br/>Give it an exec pipe for stderr and Run<br/>blocks until the user copies something else"]:::evidence

    Q12{"X5 / M4<br/>What keeps an Android device reachable?"}:::question
    Q12 ==chosen==> D56["D-56 · a foreground service holding the listener,<br/>with the offer prompt on a notification.<br/>No IPC: D-31 puts the core in this process,<br/>so the daemon is a service, not a socket"]:::accepted
    Q12 -.rejected.-> R12A["listener owned by the activity<br/>the phone stops receiving the moment<br/>the screen closes, which is most of the time<br/>somebody wants to send it a file"]:::rejected
    D56 -.constrained by.-> D56N["Android 10+ ignores a background<br/>clipboard write, silently. The notification<br/>is what makes received content reachable"]:::evidence

    Q7{"ADR-6<br/>Keep consensus and replication?"}:::question
    Q7 ==chosen==> D11["D-11 · dropped<br/>every capability is a pairwise session;<br/>pairwise needs no agreement protocol"]:::accepted
    Q7 -.rejected.-> R7A["keep a light replication layer<br/>for hypothetical N-over-2 features<br/>speculative, and never gets removed"]:::rejected

    classDef question fill:#1565c0,color:#fff,stroke:#0d47a1
    classDef accepted fill:#2e7d32,color:#fff,stroke:#1b5e20
    classDef rejected fill:#b71c1c,color:#fff,stroke:#7f0000
    classDef open fill:#ef6c00,color:#fff,stroke:#e65100
```

## What is still open

```mermaid
flowchart LR
    N1["D-8 · local unlock for Owned access"]:::open --> W1["blocks the trust store schema,<br/>which Phase 1 writes early"]:::evidence
    N2["BBRv1 vs v2/v3 under D-14"]:::open --> W2["blocks starting the quic-go port"]:::evidence
    N3["GSO gap — no ADR yet"]:::open --> W3["blocks the PRD G1 parity claim<br/>for Windows and macOS bulk"]:::evidence
    N4["D-9 · media plane"]:::open --> W4["blocks Phase 4 design,<br/>not Phase 1"]:::evidence

    classDef open fill:#ef6c00,color:#fff,stroke:#e65100
    classDef evidence fill:#455a64,color:#fff,stroke:#263238
```

---

# Entries

The normative record. Each entry holds the reasoning a diagram cannot.


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
Status: superseded by D-18 — the gate was accepted with a specified token lifetime, which this entry left open
Context: PRD K10 and R3. Unattended "Owned" access is the feature that makes S3 (working from a hostel network against a machine nobody is sitting at) possible, and it is also the feature that makes a stolen paired laptop equivalent to owning every other machine. The open question was whether to require a second factor to *use* Owned access or to accept SSH-like semantics where possession of the key is sufficient.
Decision (proposed): Require a device-local unlock (OS biometric or PIN) to *initiate* an Owned-level session. Configurable per device, default on. Do not require re-authentication per operation within a live session.
Rationale: the threat being defended against is an unattended or stolen device, which a session-initiation gate covers. Gating each operation instead would defend against nothing extra in that scenario while making the away-from-home working session unusable.
Alternatives considered: SSH-like, key possession alone (rejected as the default — SSH keys in practice are protected by a passphrase plus an agent that caches it, which is structurally the same design as proposed here; adopting SSH's model minus SSH's passphrase habit would be strictly weaker than the comparison implies). Per-operation unlock (rejected — destroys S3, the scenario unattended access exists to serve). No second factor at all, documented as accepted risk (rejected — R3 already makes promotion to Owned a deliberate act; leaving the resulting capability entirely unguarded is inconsistent with that care).
Consequences: Requires a local-authentication adapter in the per-platform shells on all three OSes, joining `Clipboard`, `Notifier`, `Capturer` and `Injector`. Adds a field to the trust-store record, which is why this must be settled before the trust store schema is written — retrofitting a schema change across already-paired devices is a migration worth avoiding. This is a product tradeoff rather than an engineering one, so it is flagged for explicit sign-off rather than resolved by evidence.

## D-9: ADR-4 — Media plane decision deferred until the bulk path is settled
Date: 2026-07-26
Status: proposed — open, lean moved to option C by D-24. Was blocked on D-12; D-14 resolved that by keeping bulk on the one connection, and D-24 then removed the scheduling argument that favoured datagrams
Context: HLD ADR-4 leans toward QUIC datagrams for the `mirror` capability, with a Moonlight-style raw RTP-over-UDP sidecar as the fallback if datagrams cannot hold latency. D-3's spike measured streams only; datagrams were not exercised, so no direct evidence exists yet.
Decision (proposed): Still try datagrams first, but decide this only after D-12, because two findings from D-4 change the inputs. First, QUIC's CPU cost of 15–25 CPU-s/GiB is comfortable at mirror bitrates on a desktop but is an open question on Android at high bitrate, feeding D-10. Second, and more structurally: RFC 9221 datagrams are congestion-controlled by the connection they ride on, so on a single QUIC connection the mirror stream shares one congestion controller with everything else — including bulk file transfer. That is the same single-controller property that sank bulk throughput in D-4. If D-12 moves bulk off this connection, the contention disappears and datagrams look considerably better; if it does not, `mirror` and `files` compete for one congestion window and HLD's priority classes have to carry the entire burden of keeping latency bounded.
Alternatives considered: Commit to the raw RTP/UDP sidecar now (rejected — it introduces a second NAT-traversal surface and a second crypto surface to audit, which is precisely what one-connection-per-peer exists to avoid; the datagram path has not been shown to fail, only shown to be coupled to an unresolved decision).
Consequences: Needs its own spike once D-12 lands, measuring datagram goodput and latency under loss, and specifically latency while a bulk transfer shares the same connection. `oabench` already has the shaped lab; it needs a datagram mode and a latency histogram.

## D-10: ADR-5 — Android core is the gomobile-bound Go core
Date: 2026-07-26
Status: superseded by D-31 — the decision stands and is now evidenced; gomobile binds quic-go at 8.4 MB per ABI
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
Status: superseded by D-14 — the option analysis below stands as the record of what was weighed; option (b) was chosen
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

## D-14: ADR-7 resolved — vendor quic-go and add BBR; one connection per peer stands
Date: 2026-07-26
Status: accepted (supersedes D-12)
Context: D-12 left the bulk transport path open with three options. D-4 and D-13 established that a single Cubic-driven QUIC connection is pinned near the Mathis limit on lossy paths — 5.5 Mb/s on `wan-relay` against parallel TCP's 27.3 — and that adding streams inside that connection does not help and past two streams actively hurts. Maintainer decision, supported by a BBR-versus-multiplexed-CUBIC model showing BBR holding near line rate across a loss range where aggregate CUBIC decays steeply: take option (b).
Decision: Vendor quic-go and add a BBR congestion controller for the bulk data path. HLD principle #1 — one connection per peer — stands unchanged for every capability, `files` and `remotefs` included. Options (a) multiple QUIC connections and (c) parallel-TCP bulk mode are not pursued.
Rationale: BBR estimates bottleneck bandwidth and round-trip propagation time and paces to those estimates, rather than treating every loss as a congestion signal. Non-congestive loss — wireless bit errors, relay drops — therefore does not shrink its sending rate the way it shrinks Cubic's. This is consistent with the published BBR results, which show the same shape as the model that prompted this decision. Keeping one connection per peer preserves NAT traversal, connection migration (PRD R9) and the session-layer priority classes for bulk as well as control, which neither (a) nor (c) does.

What this decision does **not** resolve, stated explicitly so it is not mistaken for settled:
- **GSO.** Per D-13 finding 4, quic-go's `UDP_SEGMENT` path is Linux-only by construction. BBR changes the congestion window; it does not change the send path. A Windows sender measured at 446 Mb/s on the `lan-1g` profile is syscall-bound, not congestion-bound, and BBR will not lift that ceiling. PRD K1 and the G1 parity bar remain open and now need a separate answer — this decision narrows the bulk problem to one platform rather than eliminating it.
- **The model is a model.** The chart behind this decision is a simulator; its BBR line sits at exactly link rate across the entire loss range, whereas real BBR has a bandwidth-probing cycle and does not achieve full utilisation. The port must be validated with `oabench` on the `wan-relay` profile against D-13's table before the gap is considered closed.

Alternatives considered: (a) Multiple QUIC connections for bulk (rejected — costs principle #1 for the `files` capability, multiplies path setup, hole punching and migration by N, and per D-13 it would have to be N connections rather than N streams, making it more work than D-12 assumed). (c) Parallel-TCP bulk mode reusing v1.0's engine (rejected — two transports, two NAT stories, and TCP hole punching is materially harder than UDP. Noted honestly: it is the only option that sidesteps the GSO gap entirely, which is why that gap is now tracked as an open item above rather than treated as dismissed).

Consequences:
- quic-go becomes a vendored dependency rather than a module requirement. Every upstream release, security releases included, must be re-merged against the local congestion-control patch. This is a standing cost on a security-critical component and should be budgeted explicitly, not absorbed silently.
- Sub-decision required before implementation: BBRv1 versus BBRv2/v3. v1 is effectively loss-agnostic and matches the model's shape; v2 and v3 reintroduce loss as an input, are fairer to competing loss-based flows, and would show a smaller gain on the `wan-relay` profile. Most available Go ports are v1; hysteria's implementation is the obvious starting point.
- BBRv1 is known to be aggressive toward loss-based flows sharing a bottleneck and can sustain standing queues. On a shared relay, or a home link carrying other traffic, that is a real externality — and it sits uneasily beside v1.0's own noted throughput-versus-fairness trade-off. Worth measuring rather than assuming, particularly since a self-hosted relay may carry several of the user's own transfers at once.
- Unblocks the `files` capability. Also constrains D-9 (ADR-4): with bulk remaining on the single connection, `mirror` datagrams share one congestion controller with bulk transfers, so the media plane must now be designed under that assumption and the session layer's priority classes carry real weight rather than being a nicety.
- Follow-up work: port BBR against the `SendAlgorithm` interface in `internal/congestion`, then rerun the `oabench` `wan-relay` profile and compare against D-13. The harness and baseline already exist.

## D-15: Decision log becomes a decision tree; `decisions.md` renamed to `decision-tree.md`
Date: 2026-07-26
Status: accepted
Context: The log had reached thirteen entries with two supersession chains — D-4 into D-6 and D-12, then D-12 into D-14. Read top to bottom it no longer showed what it had actually become: a record of branch points where several options were explored and one was taken. A reader arriving fresh could not see at a glance which questions were still open, or why a rejected option still mattered. D-14's GSO caveat, for instance, only makes sense if you know that option (c) was the one branch that would have avoided it.
Decision: Rename `docs/decisions.md` to `docs/decision-tree.md` and place Mermaid decision trees at the top as an index, above the unchanged entries. The trees show the questions asked, the branches explored, the branch taken, and what remains open. The entries beneath stay the normative record.
Alternatives considered: Replace the prose entries with the diagrams alone (rejected — a diagram cannot carry why a branch was rejected, what was measured, or what a choice costs, and that rationale is the entire reason D-1 created the log; the shape of a decision is not the same artefact as its justification). Keep `decisions.md` and add a separate `decision-tree.md` index (rejected — two files drift apart, and an index is only trustworthy sitting beside what it indexes). Keep the flat log and rely on the Status field alone (rejected — supersession chains are exactly what a flat list renders badly, which is the problem being solved).
Consequences: `AGENTS.md` sections 1 and 3, `docs/functionality.md`, `docs/openair2-hld.md` and `oabench/README.md` updated to the new path. The trees become a maintenance surface — any agent adding an entry must add its node and update the status table in the same commit, because an index that silently disagrees with the entries is worse than no index at all; `AGENTS.md` section 1 now states this. Rendering depends on Mermaid support: GitHub renders it inline, other viewers see the fenced source, which is why the status table above the diagrams is prose rather than an image and remains readable without a renderer.

## D-16: BBRv1 for the vendored congestion controller; corrects D-14's vendoring cost
Date: 2026-07-26
Status: accepted
Context: D-14 chose BBR for the bulk path and left "BBRv1 versus BBRv2/v3" as an explicit sub-decision. On algorithm merits v2/v3 win outright: v1 ignores loss entirely, so on shallow-buffered links it induces loss and sustains high retransmit rates; it is unfair to loss-based flows sharing a bottleneck; it exhibits RTT-unfairness among BBR flows; its roughly 2×BDP inflight cap holds a standing queue; and ProbeRTT collapses inflight to a few packets for about 200 ms every ten seconds. v2 and v3 were built to fix exactly those, adding bounded loss and ECN response, a gentler ProbeRTT, and better ACK-aggregation modelling.
Decision: BBRv1, taken from hysteria's implementation. This is a decision on availability, not on merit.
Evidence, verified 2026-07-26: no BBRv2 or BBRv3 exists in Go. Upstream quic-go v0.61 has no BBR at all; the metacubex fork has none. The one maintained Go implementation is `github.com/apernet/hysteria/core/v2@v2.10.0/internal/congestion/bbr` — 2619 lines, MIT licensed and therefore GPL-3.0 compatible with attribution, and confirmed v1 by inspection: the four-mode STARTUP/DRAIN/PROBE_BW/PROBE_RTT state machine, `defaultHighGain = 2.885`, and no `inflight_hi`, loss-threshold or ECN markers anywhere. v2/v3 exist only in Linux TCP (C) and QUICHE (C++); porting one is a from-scratch implementation of a subtle algorithm, not an adaptation.
Second reason the choice costs less than it appears: **v3 would not buy much throughput on OpenAir's own profiles.** Its loss response is threshold-based at roughly 2%, while `wan-relay` is 0.5% and `cgnat-punch` is 1% — both under it. On the paths this project actually models, v3 would hold throughput much as v1 does. The gap between them here is fairness and queueing, not goodput.
**Correction to D-14.** That entry states vendoring means maintaining a patch against quic-go's `internal/` package, with every upstream security release re-merged. That is no longer the cheapest path. Hysteria's BBR imports `github.com/apernet/quic-go/congestion` — an *exported* package — and installs it on a live connection with `congestion.UseConfigured(conn, type, profile)`. The apernet fork has already solved the injection problem by making congestion control public API. The dependency therefore becomes apernet/quic-go plus hysteria's BBR, and the maintenance burden shifts from carrying a local patch to tracking someone else's fork. That fork sits at v0.60.1 against upstream v0.61.0, so it is roughly current, but the risk changes shape rather than disappearing: a fork that stops tracking upstream is a security-relevant dependency going stale, and that needs watching.
Alternatives considered: Port BBRv3 from QUICHE (rejected — research-grade effort on a subtle algorithm, for a throughput gain that the threshold analysis above says would be near zero on these profiles). Wait for upstream quic-go to add BBR (rejected — no indication it is coming, and it would block the `files` capability indefinitely). Use hysteria's "Brutal" fixed-rate controller, which ships alongside its BBR (rejected — it disregards congestion signals by design, which is defensible for a circumvention tool on a path you have measured, and indefensible as the default for a general file-transfer product on other people's networks).
Consequences: The one v1 defect that genuinely bites this architecture is queueing, not fairness. D-14 keeps bulk on the same connection as clipboard, input and eventually `mirror` datagrams, and BBRv1's standing queue and ProbeRTT dips add latency to everything sharing that congestion controller — RFC 9221 datagrams bypass stream flow control but not the pacer. That works directly against HLD's sub-50 ms glass-to-glass target and its requirement that input never queue behind bulk. Mitigation is a knob rather than a rewrite: hysteria's BBR exposes gain profiles, one constructor using `highGain: 2.25` against the `2.885` default, trading throughput for a shorter queue. The fairness objection is real but is not a regression — v1.0 already ships eight parallel CUBIC flows, which takes eight shares of a bottleneck, so a single BBRv1 flow is if anything a better network citizen than what the project ships today. This must be measured rather than assumed: `oabench` gains a latency probe (see `docs/functionality.md`) that pings on a stream and on a datagram while a bulk transfer saturates the same connection, reporting idle and busy percentiles so the queueing cost of a controller is visible instead of inferred. Cubic numbers are the baseline to beat; BBRv1 is measured against them when the port lands.

## D-17: Sharing one connection costs less interactive latency than separate connections; netem queue depth corrected
Date: 2026-07-26
Status: accepted (evidence entry; delivers the measurement D-16 asked for)
Context: D-16 flagged BBRv1's standing queue and ProbeRTT dips as the one defect that genuinely bites this architecture, because D-14 keeps bulk on the same connection as clipboard, input and eventually `mirror` datagrams, and required the cost be measured rather than assumed. `oabench` gained a latency probe: a request/response ping on a QUIC stream and over RFC 9221 datagrams, and for TCP on its own separate connection — mirroring v1.0's architecture, where control never shared a socket with bulk. Sampled for two seconds idle, then continuously while the bulk transfer saturates the link, reported as percentiles.

Method note, and a correction to the lab: the netem queue was previously a flat `limit 100000` packets — roughly 150 MB, over a hundred times the bandwidth-delay product of every profile. That was chosen to stop netem manufacturing loss on high-BDP paths and overshot into extreme bufferbloat, which inflated latency-under-load by more than an order of magnitude and measured the lab rather than the transport. `netem/lab.sh` now derives the limit from the BDP, defaulting to 4×, overridable with `BUFFER_BDP`. Throughput conclusions in D-4 and D-13 are unaffected — re-measured `wifi-5g` at 4 streams gives TCP 186.4 Mb/s against D-13's 188.1, and QUIC GSO-on 183.1 against 175.3, both within run-to-run noise — because those profiles were bandwidth- or loss-limited rather than queue-limited. Latency conclusions would have been badly wrong.

Measurements, `wifi-5g` profile, 48 MiB, 4 streams/connections, milliseconds:

| buffer | path | idle p50 | busy p50 | busy p99 | busy max | goodput |
|---|---|---|---|---|---|---|
| 4×BDP | TCP, separate connection | 6.53 | **83.71** | 109.43 | 109.43 | 186.4 |
| 4×BDP | QUIC stream, shared | 6.96 | **12.17** | 23.19 | 23.35 | 183.1 |
| 4×BDP | QUIC datagram, shared | 7.00 | 11.85 | 22.48 | 22.57 | — |
| 1×BDP | TCP, separate connection | 6.53 | 22.89 | 49.71 | **246.80** | 166.9 |
| 1×BDP | QUIC stream, shared | 7.01 | 21.79 | 29.74 | 30.24 | 187.8 |
| 1×BDP | QUIC datagram, shared | 7.02 | 21.41 | 29.33 | 30.12 | — |

Findings:

1. **Separate connections buy no latency isolation.** This is the result that matters and it is not what D-16 anticipated. At consumer-typical buffering, a ping on its own TCP connection suffers 83.71 ms while bulk runs, against 12.17 ms for a ping sharing the QUIC connection with that same bulk — a factor of seven the other way. Transport-level separation does not isolate you from queueing at the bottleneck, because the bottleneck queue is shared no matter how many connections you open. v1.0's architecture, where control has its own socket, therefore provides far less protection for interactive traffic than its shape suggests.

2. **The shared-connection design is a latency asset, not the liability D-16 implied.** One congestion controller means one entity deciding how much is in flight; four independent CUBIC flows each fill the queue with no knowledge of the others. D-16's concern about BBRv1 is not thereby void — BBR could still worsen QUIC's numbers, and that is the comparison to run once the port lands — but the premise that sharing a connection is what puts interactive traffic at risk is wrong, and the Cubic baseline it must beat is 12.17 ms p50, not TCP's 83.71.

3. **QUIC is markedly less buffer-sensitive.** Dropping from 4× to 1× BDP costs TCP throughput (186.4 down to 166.9) while still leaving it a worse tail (max 246.80 ms against QUIC's 30.24). QUIC holds goodput at both depths, 183.1 and 187.8, which is what sender-side pacing is for. Shallow buffers are the case OpenAir cannot control — other people's networks — and it is where the gap widens.

4. **Datagrams and streams behave alike under load.** 11.85 against 12.17 ms at 4×BDP. Datagrams skip stream flow control but are paced by the same congestion controller, so on this evidence they offer no latency advantage for small messages. That bears on D-9 (ADR-4): the case for a datagram media plane rests on avoiding retransmission of stale video, not on lower latency per message.

Consequences: The Cubic baseline for the BBRv1 comparison is recorded above; when the port lands, rerun `oabench send -probe` on the same profiles and compare against this table rather than against intuition. `BUFFER_BDP` is now the knob for studying bufferbloat deliberately, and the default of 4 should be stated whenever latency figures are quoted. One caveat carried from D-4: this is still a single machine with sender, receiver and netem sharing a core, so absolute latencies include scheduling noise; the comparison between transports under identical conditions is the trustworthy part. HLD's priority classes remain worth building — nothing here measures input contending with bulk *inside* one QUIC connection under stream prioritisation, only that the connection as a whole queues less than four TCP flows do.

## D-18: ADR-3 resolved — biometric/passcode gate with a 6-hour session token
Date: 2026-07-26
Status: accepted (supersedes D-8)
Context: D-8 proposed a local unlock to start an Owned-level session but left the lifetime of that unlock unspecified, which is the parameter that actually determines how long a stolen device stays useful. PRD K10 is the underlying risk: unattended access means possession of a paired device's key is possession of every machine it is paired with.
Decision: Maintainer-specified flow.

```
Start session -> biometric/passcode challenge -> success -> grant session token
   |
   +-- default: 6-hour timer
   |      |-- 6 hours elapse ----+
   |      |-- manual session end +--> invalidate local token -> re-auth required for next access
   |
   +-- opt-in "never": active until manual revoke
```

The challenge gates *starting* a session, not each operation within it, preserving S3 — the away-from-home working session that unattended access exists to serve. Default expiry is six hours; "never" is available but must be opted into per device.

**Required refinement, or the gate is only a policy flag.** As drawn, the token is local state consulted by our own daemon. An attacker who has the machine can bypass a check their own copy of the software performs, because the thing that actually authenticates to peers is the Ed25519 device key from D-7, which sits on disk. To make the gate cryptographically real, the device private key must itself be sealed in the platform keystore with user-presence required — Android Keystore with `setUserAuthenticationRequired`, Windows Hello via CNG, and on Linux the TPM where present. The "session token" is then the unlocked key handle rather than a boolean, and expiry means dropping that handle so the key genuinely cannot be used until the user authenticates again. Without this the design raises the bar against casual theft — a stolen unlocked laptop, someone sitting down at your desk — but not against an attacker who can read the filesystem. Which of those two properties is being claimed must be stated plainly in the threat model PRD R29 requires; they are very different guarantees.

**Platform gap.** Biometrics are not uniformly available. Android has BiometricPrompt and Windows has Hello, both solid. Linux has no standard biometric API — fprintd coverage is patchy and polkit is not a biometric prompt — so the guaranteed path there is the passcode branch of the diagram. That means OpenAir defines and verifies a credential of its own on Linux: a PIN, which must be stored as a KDF hash (argon2id) and never alongside the sealed key, with rate limiting on attempts. That is new attack surface introduced by this decision and it belongs in the threat model rather than being discovered during implementation.

Alternatives considered: Key possession alone, SSH-like (rejected in D-8 and still rejected — SSH keys carry a passphrase plus an agent in practice, and adopting the model without that habit is strictly weaker than the comparison suggests). Unlock per operation (rejected in D-8 — destroys S3). A sliding rather than absolute six-hour window (rejected — a sliding window means an attacker who keeps the session active is never locked out, which inverts the purpose; the timer is absolute from grant).

Consequences and questions this decision creates, to settle before the trust store schema is written:
- **Token scope.** Per paired peer, or one token covering all Owned devices? Per-peer is the stronger property and the more annoying one. Unspecified above; needs a call.
- **Behaviour at expiry with work in flight.** A 20 GB transfer at the 5h59m mark should not be destroyed by the timer. Proposed: expiry blocks *new* operations while permitting in-flight ones to finish, since those were authorised when they began. This interacts with PRD R13's resumable transfers and should be explicit.
- **"Never" must be as deliberate as promotion to Owned.** PRD R3 already makes promotion an explicit act on the target machine; an unlimited token should be equally visible and equally revocable, and ought to appear in the paired-device list rather than hiding in settings.
- **Auth events belong in the session log.** PRD R4 gives the accessed device a visible indicator and a local log. Unlock, expiry and manual end are exactly the events that log exists to make auditable, and they are initiator-side, so both ends need a record.
- Trust store record gains the fields this implies: `authPolicy` (timed | never), `tokenGrantedAt`, and whether the device key is keystore-sealed on this platform — the last because it will not be true everywhere at first, and a peer should be able to tell.
- Adds a `LocalAuth` adapter to the per-platform shells alongside `Clipboard`, `Notifier`, `Capturer` and `Injector`.

## D-19: Key-at-rest design for D-18 — keystore or Argon2id, converging on an in-RAM Ed25519 key
Date: 2026-07-26
Status: accepted, with two unresolved items called out below
Context: D-18 recorded the auth gate and 6-hour token, and flagged that as drawn the token was local state our own daemon consults — bypassable by anyone holding the machine, because the D-7 Ed25519 key authenticating to peers sits on disk. This entry records the maintainer's key-management design, which closes that gap.
Decision:

```
Request Owned session access
   |
   +-- OS biometrics available? --YES--> OS biometric challenge
   |                                       -> unseal K_master from platform keystore
   |
   +---------------------------- NO ----> application PIN / passcode
                                           -> derive K_master via Argon2id(PIN + salt)
   |
   +--> both converge: decrypt the Ed25519 device key into RAM,
        hold the decrypted state for 6 hours (D-18's token)
```

The Ed25519 key is stored encrypted at rest under `K_master`. The 6-hour token of D-18 is now concretely the lifetime of the decrypted key in memory, so expiry is a wipe rather than a policy check. Two acquisition paths for `K_master`, one downstream code path.

**Unresolved item 1 — inbound versus outbound. This is load-bearing and the diagram does not yet cover it.** The flow is written from the initiator's side: "request Owned session access". But the same Ed25519 key terminates TLS in *both* directions. If the home desktop's key is sealed and its token has expired, an incoming session from the laptop cannot complete the handshake, and the desktop is unreachable until somebody walks over and authenticates — which is precisely the scenario PRD G5 and S3 exist to eliminate. The gate therefore cannot apply symmetrically. Either the responder keeps its key warm continuously (in which case sealing protects the mobile, stealable device and *not* the always-on desktop, and the threat model must say so), or responders use a separate non-gated key (which weakens the property differently). The asymmetry is defensible — physical access to a running always-on machine is largely game over regardless — but it must be a stated choice, not an accident of which direction the diagram was drawn from.

**Unresolved item 2 — the two branches are not of comparable strength, and Linux is on the weaker one.** The keystore path is not brute-forceable offline: the hardware enforces attempt limits and the secret never leaves it. The Argon2id path is. A 4-to-6 digit PIN carries somewhere around 10^4 to 10^6 of entropy, and an attacker holding the encrypted key file can grind it offline; Argon2id raises the per-attempt cost but does not change the arithmetic, and memory-hard parameters tuned for a phone are affordable on a GPU. Per D-18, Linux has no standard biometric API and therefore defaults to exactly this branch — so the platform likeliest to be the primary development machine, and to hold the most valuable Owned access, has the weakest gate. Options, none free: require a passphrase rather than a numeric PIN where no keystore exists; use the TPM on Linux where present, moving that platform onto the sealed path; or accept the gap explicitly and document it. This needs a decision before implementation.

Refinement worth taking on the keystore path: extracting the key into RAM at all is the weaker of two available designs. Android Keystore and Windows CNG can hold a key and perform signatures *inside* the keystore, so the private key never enters the process. Go's `tls.Certificate.PrivateKey` accepts any `crypto.Signer`, so a keystore-backed signer is compatible with the D-7 TLS design without changing it. Better still, keystore APIs can authorise a key for a bounded period after user authentication, which maps directly onto D-18's six hours and is enforced by hardware rather than by a `time.Timer` in our process that an attacker could patch out. Where this is available it should be preferred, with the decrypt-into-RAM path as the fallback for platforms that lack it.

Implementation requirements this creates:
- Holding a decrypted private key in Go memory needs deliberate handling: the runtime copies and moves allocations freely, and a key can reach swap or a core dump. Lock the pages (`mlock`/`VirtualLock`), disable core dumps for the daemon, and zero the buffer on expiry, manual end and shutdown. "Hold decrypted state for 6 hours" is the entire security boundary, so this is not a detail.
- The at-rest format needs specifying in `PROTOCOL.md` alongside the wire format: an AEAD (XChaCha20-Poly1305 or AES-256-GCM), the salt stored beside the ciphertext, and **versioned Argon2id parameters** so cost can be raised later without stranding existing installs.
- PIN change re-encrypts the key under a newly derived `K_master`. A forgotten PIN is unrecoverable and means re-pairing every device — consistent with D-7's pinning semantics, but it is a user-visible consequence that belongs in the UI, not a surprise.
- Rate limiting on the PIN path must live wherever the ciphertext does not, or it is trivially skipped by copying the file elsewhere. On-device limiting protects the interactive path only; it does not protect against offline attack, which is item 2 above.

## D-20: Two keys per device — a warm identity key and a gated privilege key; "never" designates always-on
Date: 2026-07-26
Status: accepted (resolves D-19 open item 1)
Context: D-19 left the inbound-versus-outbound question open. One Ed25519 key terminating TLS in both directions cannot both be sealed behind user presence and keep a machine reachable while nobody is there — the two requirements are irreducibly opposed, and PRD G5 and S3 depend on the reachability half.
Decision: Every device holds two Ed25519 keys.
- **Identity key** — the one peers pin per D-7. Terminates TLS, always usable, never gated. It keeps the device reachable and authorises the capabilities granted at pairing: clipboard push, notification mirroring, inbound transfer offers. Trusted level.
- **Privilege key** — encrypted at rest per D-19, unsealed only by D-18's challenge, and live exactly as long as the six-hour token. Required to initiate or authorise Owned-level unattended operations: remote filesystem browse, screen mirror, remote input, unattended pull.

D-18's opt-in "never expire" *is* the always-on designation — one user-visible toggle, not two. A device set to "never" keeps its privilege key unsealed continuously, sealed to boot state in the TPM so it auto-unseals with no human present. A device on the six-hour default re-locks and is simply not remotely controllable while locked, which for a laptop in a bag is the desired behaviour rather than a limitation.

Why this resolves the tension: the identity key never locks, so reachability never depends on anyone being present, and the gate sits on the dangerous capability instead of on the transport that carries everything.

**Consequence worth stating up front, because it nearly went the other way:** the continuity features run on the identity key. Notification mirroring (R21) and clipboard sync (R19) therefore keep working while the privilege key is locked. Had they been gated, a phone left idle for six hours would silently stop mirroring notifications — the gate would have become visible in precisely the place a user would least tolerate it, and the feature would have been blamed rather than the policy.

Android note: Keystore can authorise a key for a bounded period following *device* unlock, so the six-hour window can key off the user unlocking their phone rather than an in-app prompt. Hardware-enforced, and close to free in UX terms.

Alternatives considered: A single key with role-based gating (rejected — role is a property of a session, not a device; a laptop is the initiator when reaching the desktop and the responder when the phone reaches it, so any per-device rule is a convenient fiction). An ephemeral TLS key signed by the long-term key at unlock, SSH-certificate style (rejected — it converts D-7's flat key pinning into a one-level chain, and after six hours the responder's certificate expires and the machine is unreachable again, arriving back at this problem by a longer route).

Consequences:
- Two keys to generate, store and revoke. Revocation must distinguish "revoke Owned privilege, keep the pairing" — a demotion to Trusted — from a full unpair, which PRD R5 already implies but did not previously have a mechanism for.
- Pairing exchanges both public keys; the trust store records both.
- `PROTOCOL.md`: Owned-level capability requests carry a signature from the privilege key, verified by the session layer's authorisation middleware before the request reaches a capability plugin.
- A stolen device whose identity key is warm can still impersonate the owner at Trusted level — offer files, push clipboard — with consent required on the far end. That blast radius is bounded but real, and belongs in R29's threat model rather than being left implicit.
- A device with no TPM or keystore cannot safely be designated always-on, since its privilege key would sit unprotected at rest. Handled by D-21.

## D-21: Protection tiers for the privilege key; Owned level requires a protected key
Date: 2026-07-26
Status: accepted (resolves D-19 open item 2)
Context: D-19 left open that its two branches are not of comparable strength. A platform keystore resists offline attack because the hardware enforces attempt limits; a numeric PIN through Argon2id does not, since an attacker holding the ciphertext can grind it offline — roughly days single-threaded for six digits at one second per attempt, and hours parallelised. Per D-18, Linux defaults to the PIN branch, which put the likely primary development machine on the weaker path.
Decision: Three tiers, in order of preference per device.
1. **Platform keystore or TPM 2.0** — Android Keystore, Windows Hello via CNG, TPM on Linux. Attempt limits are enforced in hardware, so offline brute force is not available at all.
2. **Passphrase, not a numeric PIN**, through Argon2id, where no keystore or TPM exists. Four diceware-style words is roughly 51 bits, which at about one second per attempt is computationally out of reach offline. Costs UX every six hours, and applies only to the minority of machines tier 1 does not cover.
3. **Neither available** — the device may pair and operate at Trusted level, but holds no privilege key. It therefore cannot initiate Owned-level operations and cannot be designated always-on under D-20.

Verified 2026-07-26 on the maintainer's Fedora development machine: TPM 2.0 present (`/dev/tpm0`, `/dev/tpmrm0`, EFI system). The primary Linux machine lands in tier 1, so tier 2 is a fallback for unusual hardware rather than the common Linux case D-18 had assumed.

What makes tier 1 sufficient, and why this matters less than it first appeared: a PIN's offline weakness bites only under the *cold* threat — a stolen powered-off disk, a leaked backup, a home directory synced to cloud storage. Against a live-compromised machine nothing at this layer helps, because an attacker with filesystem access can equally keylog the credential the next time it is typed. Tier 1 closes the cold threat completely; no tier closes the warm one, and pretending otherwise would be the more dangerous error.

Alternatives considered: A numeric PIN with aggressively raised Argon2id parameters as the sole protection (rejected — mitigation rather than a fix; memory-hard parameters tuned to be tolerable on a phone are affordable on a workstation GPU). Accept and document the gap without changing behaviour (rejected — tier 3 is the honest form of that, aligning the capability granted with the protection actually available rather than shipping a weak gate under a strong-sounding name).

Consequences:
- The tier must be recorded in the trust store *and* visible to peers, so a device deciding whether to grant Owned access can see whether the requesting device actually protects its privilege key. Joins the fields D-18 and D-20 add.
- The UI must state tier 3 plainly rather than silently degrading; a user who believes they have unattended access and does not is worse off than one who was told.
- Argon2id parameters are versioned in `PROTOCOL.md` per D-19, so cost can be raised later without stranding existing installs.
- Linux TPM work is two policies, not one: sealing to PCRs for the always-on case, where the key auto-unseals at boot bound to boot state and no human is present, and sealing with user presence required for the interactive case. D-20 needs both, and they are separate implementations.

## D-22: ADR-8 — implement Windows UDP Send Offload in the vendored quic-go
Date: 2026-07-26
Status: accepted
Context: D-13 established by code inspection that quic-go's `UDP_SEGMENT` support is Linux-only by construction — `isGSOEnabled` returns a hardcoded `false` on darwin and freebsd, and `appendUDPSegmentSizeMsg` is a no-op stub in `sys_conn_helper_nonlinux.go`. Windows therefore sends one packet per syscall, and measured at roughly half of TCP's throughput on every emulated profile: 93 against 188 Mb/s on `wifi-5g`, 446 against 952 on `lan-1g`. D-14's BBR decision does not touch this, because BBR changes the congestion window and this is a send-path cost. Scope is narrower than earlier entries implied: `GOOS=android` satisfies the `linux` build tag, so Android compiles the GSO path, and macOS is best-effort under PRD G1. Windows is the only first-class platform affected.
Decision: Implement Windows UDP Send Offload in the quic-go fork D-14 already commits the project to. Windows has USO via the `UDP_SEND_MSG_SIZE` socket option since Windows 10 1709 and Server 2019; quic-go simply does not use it. Offer the work upstream — this is a gap in the library rather than something specific to OpenAir, and carrying it locally forever is worse than trying to hand it back.

Structure verified 2026-07-26, which is what makes this tractable rather than speculative: `gsoSize uint16` is **already threaded through quic-go's platform-agnostic send path**. It appears in the `rawConn.WritePacket` interface in `sys_conn.go`, in `sconn.Write` and `writePacket` in `send_conn.go`, and in `sendQueue.Send`. Windows is excluded from both `sys_conn_oob.go` (tagged `darwin || linux || freebsd`) and `sys_conn_no_oob.go` (which excludes windows explicitly), so it already has its own `sys_conn_windows.go`. The parameter reaches the platform layer and is discarded there. The hook exists; the implementation behind it does not.

One mechanism difference the implementer should expect rather than discover: Linux carries the segment size **per send, as an OOB control message**. Windows `UDP_SEND_MSG_SIZE` is a **sticky socket option** set with `setsockopt`. So this is not a port of the cmsg code but a different mechanism reaching the same effect through an abstraction that already accommodates both. In practice the segment size is stable for the life of a connection, so setting it only when it changes costs nothing measurable.

Alternatives considered: Accept the gap and document it (rejected — PRD G1 makes parity the bar for Windows, and half throughput on bulk transfer is not parity). Use the v1.0 TCP engine for bulk on Windows only (rejected — this is option (c) from D-12, already rejected there for requiring two transports and two NAT stories, and made worse by being platform-conditional). Wait for upstream quic-go to add USO (rejected — no indication it is coming, and the fork already exists for BBR).

Consequences:
- **Measure before implementing, not as a gate but as a baseline.** The 93-against-188 figures come from a single machine with sender, receiver and netem contending for one core, which penalises the CPU-hungrier transport. `oabench` cross-compiles, so `GOOS=windows go build` and one session on the actual Windows laptop establishes the real pre-fix number. Without it there is no way to demonstrate afterwards that USO worked, only a belief that it should have.
- **The receive side is separate work.** Linux coalesces receives with `UDP_GRO`; Windows has URO via `UDP_RECV_MAX_COALESCED_SIZE`, which quic-go does not use either. OpenAir moves bulk data in both directions, so a device receiving a large transfer on Windows pays the same per-packet cost this entry fixes for senders. Tracked as follow-up rather than folded in, since the two have independent implementations and independent risk.
- `x/sys/windows` does not define these constants; they will need declaring in the fork.
- Fold into the same fork as the BBR work from D-16, so there is one patch set against one upstream to re-merge rather than two.
- If upstream accepts the contribution, the local carrying cost for this piece disappears — a reason to raise a PR early rather than after it has diverged.

## D-23: Windows offload is one piece of work, not USO now and URO later
Date: 2026-07-26
Status: accepted (extends D-22's scope; D-22's decision to implement USO is unchanged)
Context: D-22 recorded the Windows receive side as a follow-up to be tracked separately, on the reasoning that send and receive have independent implementations and independent risk. Inspecting the code rather than assuming shows that reasoning was wrong, and in two ways.
Findings, verified 2026-07-26 against quic-go v0.61:
- Windows does not merely lack send offload. It falls back to `basicConn`, the generic path with **no optimisation of any kind**: `ReadPacket` performs a plain `ReadFrom`, one syscall per packet, and `WritePacket` contains `panic("cannot use GSO with a basicConn")`. The batched-receive machinery (`batchConn`, `ReadBatch`) lives in `sys_conn_oob.go`, tagged `darwin || linux || freebsd`.
- This corrects D-22's framing that "the hook exists; the implementation does not". The `gsoSize` *parameter* does reach the platform layer, but the platform layer on Windows is `basicConn`, which actively rejects it. The work therefore includes introducing a Windows connection type that is not `basicConn` — and that type is the shared prerequisite for both directions.
Decision: Treat Windows offload as a single piece of work — one Windows fast-path connection implementing both USO on send (`UDP_SEND_MSG_SIZE`) and URO on receive (`UDP_RECV_MAX_COALESCED_SIZE`) — rather than shipping send now and receive later.

Three reasons, in order of weight:

1. **They share their only hard prerequisite.** Both need a Windows connection type replacing `basicConn`. Once that exists, adding the second socket option is incremental rather than a second project. Splitting the work means paying the prerequisite once but carrying two patch sets against an upstream that has to be re-merged on every release.
2. **On Windows, URO *is* the batching mechanism.** There is no `recvmmsg` equivalent; `WSARecvFrom` returns one datagram at a time. `UDP_RECV_MAX_COALESCED_SIZE` is how a single receive call returns many datagrams. So URO is not an incremental optimisation on top of batched receive the way GRO is on Linux — it is the only way Windows gets more than one packet per syscall, which makes it structurally more important there than its Linux counterpart.
3. **The PRD's Windows machine is predominantly a receiver.** S2 has the Windows laptop receiving a 2 GB build artifact; S3 has it pulling files from the desktop and viewing a screen mirror. Shipping send offload alone would optimise the direction that persona uses least.

Correction to how D-13's numbers should be read: those runs disabled GSO on both processes, but both were on Linux, and GSO is send-side only — so the receiver retained Linux's batched receive throughout. The measurement was a crippled sender talking to an optimal receiver. Real Windows-to-Windows has an unbatched receiver as well, so **93 Mb/s on `wifi-5g` and 446 on `lan-1g` are optimistic bounds for Windows, not pessimistic ones.** D-4's co-location caveat pushes the other way, which is exactly why the pre-fix baseline on real hardware that D-22 requires cannot be skipped: two errors of unknown size point in opposite directions and only a measurement separates them.

Alternatives considered: Ship USO first and treat URO as a follow-up, per D-22 as written (rejected on the three points above, principally that the prerequisite is shared so the split saves nothing and costs a second re-merge). Implement only URO, on the grounds that the Windows device is mostly a receiver (rejected — bidirectional transfer is a first-class case, and a machine that receives well but sends at half rate is a worse product than one that does neither, because the failure becomes intermittent and direction-dependent rather than consistent). Wait for upstream to build a Windows fast path (rejected for the same reason as in D-22 — no sign of it, and the fork already exists).

Consequences:
- The scope of the ADR-8 work becomes: one Windows connection type, plus `UDP_SEND_MSG_SIZE`, plus `UDP_RECV_MAX_COALESCED_SIZE`, plus the constants that `x/sys/windows` does not define. Larger than D-22 implied, but a single coherent contribution rather than two partial ones — and correspondingly more attractive to upstream, which is where it should end up.
- The baseline session on real Windows hardware should measure **both directions**, Windows-as-sender and Windows-as-receiver. `oabench` already supports this by choosing which end runs `serve`; no harness change is needed.
- Success criterion for ADR-8 is now bidirectional: Windows within reach of the Linux figures on the same profile in both roles, not just as a sender.

## D-24: Bulk quiesce at the session layer replaces transport-level priority classes
Date: 2026-07-26
Status: accepted
Context: HLD section 3.4 specifies priority classes — `interactive` above `media` above `bulk` — "enforced via quic-go stream priorities + sender-side pacing of bulk writers". Verified 2026-07-26: **quic-go v0.61 exposes no stream prioritisation of any kind.** There is no `SetPriority`, no priority field, nothing exported; the framer round-robins streams. The mechanism the HLD names as the enforcement point does not exist, and building one would be a third patch against the vendored fork alongside BBR (D-16) and Windows offload (D-22, D-23).

Two measurements bound how much of a problem that actually is:
- D-17 showed small interactive messages already cross a saturated connection at 12.17 ms p50 and 23 ms p99, against a 7 ms idle baseline. Clipboard, notifications and input are small and infrequent, so the `interactive` class does not need prioritisation — it already works unprioritised.
- `packet_packer.go` packs DATAGRAM frames before stream data unconditionally, so datagrams already receive de-facto priority. That helps only the media plane, and only if the media plane is built on datagrams.

What remains is a single case: sustained high-bitrate media competing with bulk in the same direction.

Decision: The session layer quiesces bulk transfer when a high-bandwidth capability is active — throttled to a floor rather than stopped outright — instead of relying on transport-level priorities. This lives entirely above quic-go, because we control when bulk writers write, so it requires no third patch to a security-critical vendored dependency.

Specifics:
- **Throttle to a floor, not a hard pause.** S3 is a multi-hour remote working session; stopping bulk entirely would leave a large transfer making no progress for hours. A floor keeps it moving at a cost the mirror will not perceive.
- QUIC congestion-controls each direction independently, so contention exists only when bulk and media flow the *same* way. A laptop-to-desktop upload does not compete with a desktop-to-laptop mirror at all.
- When they do contend, the machine that must throttle is the *sender* of the bulk data, which is not necessarily the one initiating the mirror. `PROTOCOL.md` therefore needs a quiesce request carrying a scope and a resume trigger — this is a wire message, not local policy.
- Arbitration belongs to the session layer, which HLD already puts in charge of flow priority. It simply becomes an application-level scheduler rather than a transport feature.

Alternatives considered: Add a priority scheduler to the vendored fork (rejected — a third patch on a security-critical dependency to solve a problem the application layer can solve directly, and D-17 shows most of the problem does not exist to begin with). Rely on the packer's datagram priority alone (rejected — it covers only the media plane, and it forces the media primitive choice as a side effect rather than on its merits). Leave contention unmanaged (rejected — sustained media against same-direction bulk is precisely the case D-17 does not cover).

Consequences:
- **This changes D-9's lean from A to C.** The strongest remaining argument for datagrams was scheduling: they are packed ahead of stream data. With bulk quiesced there is nothing to be scheduled ahead of, so that argument disappears. Stream-per-frame with `RESET_STREAM` keeps free fragmentation and reassembly, keeps flow control, and avoids the 32-slot datagram send queue whose overflow is a silent discard. D-9 remains open pending its spike, but the spike should now treat C as the favourite rather than A.
- Two interactions to measure once the BBR port lands. Throttling depresses BBR's delivered-rate estimate, so resuming requires re-probing, and repeated cycles may keep the controller unsettled — a direct interaction between two decisions taken separately. And quiesce is not instantaneous: up to a full congestion window is already in flight, roughly 1 MB on the `wan-relay` profile, so a round-trip-scale latency spike at mirror start should be expected rather than treated as a bug.
- HLD section 3.4's claim that priority classes are enforced via quic-go stream priorities is factually wrong and is corrected in the same commit as this entry.

## D-25: Authorisation lifecycle — hybrid consent, session announce, capped expiry grace
Date: 2026-07-26
Status: accepted
Context: `PROTOCOL.md` specified Owned-level authorisation thoroughly (§6) and left three things implicit that PRD requirements depend on. Trusted is the *default* level at pairing, so its consent path is the more common one and had no messages at all. PRD R4 requires the accessed device to show an indicator, keep a session log and let a local user kill a session instantly — none of which was signalled. And D-18's six-hour expiry had no defined behaviour for work already running.
Decision:
- **Consent is hybrid.** Capabilities granted at pairing or later are persistent and prompt nothing; anything ungranted prompts once per session. PRD R3 permits "per-session or per-capability", and this is both. It matches platform app-permission behaviour, which users already have a model for. Granted scope may narrow what was requested but never widen it, and a denial cannot be re-requested within the session — prompt fatigue is an attack, not an inconvenience.
- **Sessions are announced.** `SessionAnnounce` precedes the first Owned operation and any use of `input` or `mirror`; `SessionEnd` closes it. Both ends log announce, end, kill and authentication events, because auth events originate on the initiator (D-18) and neither log is sufficient alone.
- **`SessionKill` is a courtesy message, not the enforcement.** A local user killing a session takes effect by the accessed device refusing further operations and resetting streams. A misbehaving peer cannot ignore its way out, because enforcement is local. Specifying it the other way would have made R4's guarantee depend on the goodwill of the party being revoked.
- **Expiry does not abort work in flight, but the grace is capped at one hour.** Operations already running were authorised when they began, and destroying a 20 GB transfer near completion serves nobody. Without a cap, starting a long operation just before expiry would extend access indefinitely — exactly what the timer exists to prevent. Users are notified 15 minutes before expiry so a long transfer can be extended deliberately rather than discovered broken.
Alternatives considered: Per-operation consent (rejected — unusable, and trains users to approve reflexively). Persistent-only consent (rejected — nothing could be tried without first being granted permanently). Hard-kill at expiry (rejected — hostile, and makes long transfers unusable near a boundary the user cannot see). Unbounded grace (rejected — trivially exploitable).
Consequences: `PROTOCOL.md` §6.2–6.5 and §8.5 specify the messages. Persistent grants are trust-store state and must appear in the paired-device list as revocable. The 15-minute warning needs a UI surface on every platform. The one-hour cap is a policy constant that belongs in configuration, not in the wire format.

## D-26: Repo layout — one root module for v2, `openair-gui` stays separate
Date: 2026-07-26
Status: accepted
Context: The v2 tree does not exist. `oabench` and `openair-gui` are separate modules, and no decision recorded where v2 code should live — which an LLD cannot avoid answering.
Decision: A single root module `github.com/shreyashsri79/openair`, with `cmd/openaird`, `cmd/openair`, `cmd/oabench` and `internal/{identity,discovery,conn,session,caps,...}`. `openair-gui` remains its own module until v1.0 is retired. `oabench` graduates from its standalone module into `cmd/oabench`.
Alternatives considered: One module for the whole repository including the GUI (rejected — Fyne pulls GL bindings and a large dependency tree, and the daemon's `go.mod` should not carry them; a headless server install would drag in graphics dependencies for nothing). Multiple modules within v2, splitting core from bindings (rejected — premature. Module boundaries create version-skew and release-ordering work, and at this size buy isolation nobody needs. `gomobile bind` operates on a package, not a module, so D-10 is unaffected). Leave v2 alongside v1 as a third peer module (rejected — that is the shape D-2 already deleted for causing unclear ownership).
Consequences: `oabench`'s import path changes when it moves; its benchmark results and netem lab are unaffected. v1.0 keeps building and shipping untouched throughout, which was the point of keeping the spike additive. When v1.0 retires, `openair-gui` either folds into the root module or is deleted outright.

## D-27: quic-go is consumed as a fork plus a `replace` directive
Date: 2026-07-26
Status: accepted
Context: Four decisions (D-14, D-16, D-22, D-23) commit the project to a modified quic-go carrying three patches — BBR, Windows USO, Windows URO — without specifying what "the fork" mechanically means.
Decision: A fork repository, consumed with a `replace` directive and tagged `v0.61.0-openair.N`, rebased onto upstream tags rather than merged.

```
replace github.com/quic-go/quic-go => github.com/shreyashsri79/quic-go v0.61.0-openair.1
```

Alternatives considered: Vendor the source in-tree (rejected — every upstream update becomes a manual merge against copied files, and the patch set stops being reviewable as a diff). Git submodule (rejected — extra moving parts, and Go tooling handles `replace` natively). Wait to upstream everything first (rejected — the Windows work is plausibly acceptable upstream but BBR is a larger conversation, and Phase 1 cannot block on someone else's review cycle).
Consequences: `replace` does not propagate to consumers of a module, which would be disqualifying for a library and is irrelevant for an application — nothing imports OpenAir. Rebasing rather than merging keeps the three patches as distinct, individually upstreamable commits, which matters because D-22 intends to offer the Windows work upstream. Each upstream release requires a rebase, and a fork that stops tracking upstream is a security-relevant dependency going stale (D-16) — this needs a periodic check, not good intentions. This is the same approach apernet/hysteria takes for the same reason.

## D-28: Protobuf toolchain is `buf`, with generated code committed
Date: 2026-07-26
Status: accepted
Context: `PROTOCOL.md` commits to protobuf for structured messages. Nothing said how `.proto` files are compiled, where generated code lives, or whether it is checked in.
Decision: `buf` for linting, generation and breaking-change detection. Generated Go is committed to the repository.
Alternatives considered: `protoc` with plugins (rejected — requires every contributor to install a matching toolchain, and provides no breaking-change detection). Generating at build time rather than committing (rejected — a plain `go build` should work on a fresh clone with only the Go toolchain, and committed output makes wire-format changes visible in review, which is exactly where they should be caught).
Consequences: `buf breaking` runs in CI against the previous release. This is directly load-bearing for PRD R32: the spec's ignore-unknown rules (§3.1) are designed to survive additive change, and `buf` mechanically catches the non-additive kind before it ships. Committed generated code must be regenerated in the same commit as the `.proto` change, and CI must verify the two agree. Golden test vectors for the envelope (HLD §5) live alongside.

## D-29: Daemon-to-UI IPC reuses the session envelope instead of gRPC
Date: 2026-07-26
Status: accepted — supersedes the gRPC choice stated in HLD section 2
Context: HLD section 2 specifies local IPC as "gRPC over unix socket / named pipe". That was written before `PROTOCOL.md` existed. Now that there is an envelope, a protobuf toolchain and a message set, the calculus has changed.
Decision: Local IPC between `openaird` and its tray UI or CLI uses the **same envelope and the same messages** as the network protocol (§3), over a unix socket or named pipe.
Rationale: one wire format to specify, test and generate goldens for, instead of two. A tray UI issuing a clipboard push sends the identical message the network carries, so there is no translation layer to keep in sync and no second serialisation to reason about in the threat model. Android is unaffected — D-10 puts the core in-process via gomobile, so it has no IPC at all.
Alternatives considered: gRPC as the HLD specified (rejected — a large dependency and a second codegen path for a local socket with a single trusted client; the streaming and deadline machinery it provides is not needed here). JSON-RPC or HTTP over the socket (rejected — a third serialisation, and it would put a human-readable copy of clipboard and notification content on a socket for no benefit).
Consequences: Request/response correlation must be implemented, roughly a request-ID map, which gRPC would have supplied — perhaps a hundred lines against a dependency that would otherwise ship in every binary. The socket needs its own access control: it is a local trust boundary, and any process able to open it can drive the daemon, so filesystem permissions and, on Windows, a named-pipe ACL are a security requirement rather than hygiene. HLD section 2 is corrected in the same commit.

## D-30: Owned unlock is scoped per peer — one prompt per device per six hours
Date: 2026-07-26
Status: accepted (resolves D-18's open sub-question)
Context: D-18 established the six-hour token but left its scope undecided: does one unlock authorise Owned access to a single paired device, or to all of them? This was the last item gating the trust store schema.
Decision: **Per peer.** One unlock authorises Owned operations against one paired device for six hours. Reaching a second device requires its own unlock. Continuity features are unaffected — clipboard and notification mirroring ride the always-warm identity key (D-20), so only browse, mirror, input and unattended pull are gated at all.
Rationale: the blast-radius argument is real but modest, since an attacker with a live unlocked session mostly reaches the device being actively used. The stronger argument is that scoping makes the prompt informative. "Unlock to access `desktop-home`" states what is being authorised; "Unlock OpenAir" states nothing, and a prompt that names no target trains users to approve reflexively. Friction is small in practice: a session like S3 touches one or two devices.
Alternatives considered: Global scope (rejected — a single approval granting every machine at once, with a prompt that cannot describe what it grants). Per-operation scope (rejected in D-8 already — destroys the away-from-home session the feature exists for).

**Honest limitation, and a refinement it enables.** As specified in D-19, unlock decrypts the privilege key into RAM for six hours. A key sitting in memory can sign for *any* peer, so per-peer scope is enforced by policy in our own daemon, not by cryptography. It bounds what a well-behaved implementation does and makes the prompt meaningful; it does not stop a daemon compromised mid-session from signing for peers the user never unlocked.

That gap is closable, and this decision makes the fix fit naturally. At unlock, generate an **ephemeral per-peer keypair**, sign its public key with the privilege key to produce a delegation valid six hours *for that peer only*, then immediately re-seal the privilege key. RAM then holds ephemeral keys scoped to specific peers rather than the long-term key. Per-request `AuthProof` signatures (§6) are made by the ephemeral key; the verifier checks the delegation against the pinned privilege public key and the proof against the delegated key — an SSH-certificate shape. Authorising a new peer means another unlock, which is precisely the UX this entry already specifies.

Cost: one additional protocol message carrying the delegation, slightly more verification, and a change to both D-19 and `PROTOCOL.md` §6. **Not adopted here** — it converts per-peer scope from policy into cryptography and is worth doing, but it is a real design change rather than an obvious one, and belongs to the maintainer.

Consequences — consolidated trust store record. Fields have accumulated across five entries; this is the authoritative list, so the LLD does not have to reconstruct it:

| Field | Source | Notes |
|---|---|---|
| `device_id` | D-7 | base32(SHA-256(identity pubkey)[0:10]) |
| `identity_public_key` | D-20 | pinned; terminates TLS, never gated |
| `privilege_public_key` | D-20 | pinned; verifies Owned `AuthProof` |
| `display_name` | R5 | renameable |
| `platform` | — | linux / windows / android / darwin |
| `level` | R3 | trusted \| owned |
| `granted_capabilities` | D-25 | persistent grants; revocable from the device list |
| `auth_policy` | D-18 | timed \| never; "never" also designates always-on (D-20) |
| `token_granted_at` | D-18, D-30 | **per peer**, per this entry |
| `protection_tier` | D-21 | 1 keystore/TPM, 2 passphrase, 3 unprotected — tier 3 blocks Owned |
| `created_at`, `last_seen` | R5 | |

The trust store schema is now fully determined and Phase 1 identity work is unblocked.

## D-31: gomobile binding verified — ADR-5 moves from proposed to accepted
Date: 2026-07-26
Status: accepted (supersedes D-10)
Context: D-10 chose a gomobile-bound Go core over a Kotlin reimplementation, but stayed `proposed` because the deciding evidence was missing: no Android NDK was available, so binding friction, artifact size and build time were unknown. If gomobile could not bind quic-go, Android's Phase 1 scope and the LLD's process model both change.
Measurement, 2026-07-26. NDK 28.2.13676358 was in fact already installed — an earlier check looked for `ANDROID_NDK_HOME`, which is unset on this machine, and wrongly concluded the toolchain was absent. A package exporting an Ed25519 identity (D-7), a self-signed certificate, and a real `quic.DialAddr` with TLS 1.3 and a pinning callback (D-6) was bound with `gomobile bind`:

| Measure | Result |
|---|---|
| Binds at all | **Yes** |
| Build time, clean, one ABI | 24 s |
| AAR, `android/arm64` | 4.6 MB |
| AAR, `arm64` + `armeabi-v7a` | 9.2 MB |
| `libgojni.so`, uncompressed, per ABI | 8.4 MB |
| Generated Java API | `deviceID()`, `dial(String, long)`, `selfSignedCertLen()`; Go `error` maps to Java `Exception` |

One friction point worth recording because it costs an afternoon to diagnose: gomobile defaults to Android API 16, which NDK 28 rejects outright (`unsupported API version 16 (not in 21..35)`). `-androidapi 21` or higher is mandatory, and the failure message does not suggest the fix.

Decision: Accept ADR-5 as decided in D-10 — one Go core, bound with gomobile, shared across all platforms.
Assessment of the cost: roughly 8.4 MB installed per ABI, or about 4.6 MB of download per device once Android App Bundle splits by ABI. That is noticeable but ordinary for this class of tool; Syncthing-Android ships a Go core the same way and is materially larger. The generated API is idiomatic enough for a Compose UI to call directly, with Go errors surfacing as Java exceptions rather than needing a translation layer.
Alternatives reconsidered in light of the evidence: Kotlin reimplementation (still rejected, and now with less justification than before — the measured cost of the Go path is a few megabytes, against duplicating a security-critical wire protocol and its audit burden). Separate process plus IPC, D-10's stated fallback (not needed; retained only if a future NDK or gomobile regression breaks binding).
Consequences: On-device throughput and battery are still unmeasured — binding and size are settled, runtime behaviour is not, and PRD K5 asks for a real mid-range device rather than a Pixel. D-4's finding that QUIC costs 10–20x TCP in CPU per byte remains the live risk for PRD R30's battery budget, and it is a runtime question this spike does not answer. `-androidapi 21` must be pinned in the build tooling so the NDK failure does not recur.

## D-32: Windows measurement and ADR-8 implementation deferred to Phase 2
Date: 2026-07-30
Status: accepted
Context: D-22 and D-23 committed the project to a Windows fast path — USO on send, URO on receive — in the vendored quic-go, and required a pre-fix baseline on real hardware before the work began. That baseline needs a Windows machine and a second network host, which are not always to hand. The question is whether it must happen before the LLD.
Correcting an earlier claim in the process: it was previously argued that both the Windows baseline and the gomobile spike gated the LLD because either could invalidate assumptions baked into it. That holds for gomobile — had binding failed, Android's process model would have changed, which is structural, and D-31 settled it. **It does not hold for Windows.** ADR-8 is a performance patch isolated to the vendored fork. Whether Windows runs at 50% or 95% of TCP changes when the work is scheduled and how it is prioritised; it does not change the architecture, the wire protocol, or the shape of the LLD.
Decision: Defer the Windows baseline measurement and the ADR-8 implementation to Phase 2. The runbook and cross-compiled binaries remain in `oabench/winkit/` so the measurement can be taken whenever the hardware is free.
Alternatives considered: Block the LLD on it (rejected per the correction above — it is a scheduling input, not an architectural one). Drop ADR-8 entirely and accept degraded Windows performance (rejected — PRD G1 makes Windows first-class with parity as the bar, so this is a deferral, not a cancellation). Implement USO and URO blind, without a baseline (rejected — D-22 already recorded that without a before-number there is no way to demonstrate the fix achieved anything, only a belief that it should have).

**The real risk of deferring is not throughput — it is that Windows goes unexercised.** Developing entirely on Linux for months is how a project discovers late that its path handling, its service installation, and in this case its **named-pipe IPC** (D-29) were never run on the platform they were designed for. Throughput is a number that can be measured at any time; a codebase that has never been compiled or executed on Windows accumulates breakage that is expensive to unpick all at once.

Mitigation, adopted with this entry: **cross-compile for Windows in CI from the first commit of the v2 tree.** `GOOS=windows go build ./...` costs seconds and catches every compile-level regression — missing build tags, Unix-only syscalls, path assumptions — without needing a Windows machine at all. This does not substitute for running it, but it keeps the platform from silently rotting between now and Phase 2.

Consequences:
- ADR-8's work moves into Phase 2 alongside the rendezvous, punching and relay work. The vendored fork carries two patches (BBR, D-16) rather than three until then.
- **The Windows baseline becomes a hard Phase 1 exit blocker rather than a Phase 1 task.** PRD G1's parity bar cannot be claimed for a platform that has never been measured, so Phase 1 cannot be declared complete on Windows until this runs — deferring the work is not deferring the obligation.
- The two-machine data outstanding since D-4 now comes from the Android runbook instead (`oabench/androidkit/`), since a phone and a desktop are genuinely two machines with two CPUs. That closes the co-location caveat without waiting for a Windows session.

## D-33: Windows baseline measured — the Linux GSO-off proxy was wrong in both directions
Date: 2026-07-30
Status: accepted (evidence entry; revises D-23's reading, changes no decision)
Context: D-13 established by code inspection that quic-go has no send offload and no batched receive on Windows. D-23 predicted the Linux `QUIC_GO_DISABLE_GSO=1` runs were an *optimistic* bound for Windows, since those Linux processes retained a batched receiver while a real Windows machine has neither half. D-22 required a real measurement before implementing ADR-8. Taken 2026-07-30 on the maintainer's Windows laptop, single machine, loopback, 512 MiB, median of 2 runs.

| config | 1 stream | 4 streams |
|---|---|---|
| **Windows** TCP | 20616 Mb/s | 27753 Mb/s |
| **Windows** QUIC | **1450 Mb/s** | **647 Mb/s** |
| Linux TCP | 14410 Mb/s | 32609 Mb/s |
| Linux QUIC, GSO on | 2307 Mb/s | 2248 Mb/s |
| Linux QUIC, GSO off | 692 Mb/s | 688 Mb/s |

Findings:

1. **At one stream, Windows beats the GSO-off proxy by 2.1x — so D-23's "optimistic bound" reading was wrong.** Windows QUIC reaches 1450 Mb/s against the proxy's 692, and sits much closer to Linux with GSO enabled. Even normalising for hardware — Windows TCP single-stream is 1.43x faster than Linux on the same test, so the laptop is the quicker machine on that path — Windows lands near 1010 Linux-equivalent Mb/s, still comfortably above the proxy. The likely reason is that the two paths are not the same code: Linux with GSO disabled still traverses the OOB control-message machinery in `sys_conn_oob.go`, whereas Windows uses the leaner `basicConn` plain `WriteTo`. "Linux minus GSO" was never Windows, and it misestimates in both directions.

2. **Windows QUIC collapses as stream count rises: 1450 down to 647, a 2.2x fall.** Linux is flat in both configurations (2307 to 2248 with GSO, 692 to 688 without). D-13 found mild degradation past two streams on Linux; on Windows it is severe and it is the dominant effect in this data. At four streams Windows lands at 647, essentially the GSO-off proxy figure — which is why a four-stream-only measurement would have appeared to confirm D-23 while concealing the single-stream result entirely.

3. **Practical consequence: ADR-8 is not urgent for throughput.** 1450 Mb/s of loopback headroom at one stream exceeds any link this product realistically runs on — 5 GHz WiFi, gigabit Ethernet, a relayed WAN path. Windows QUIC will saturate all of them. ADR-8 matters for 2.5G+ links and for CPU efficiency, not for ordinary transfers. This supports D-32's deferral to Phase 2 with evidence rather than convenience.

4. **Design constraint, and the more valuable result: use one or two streams for bulk, never four or more.** D-13 already showed extra streams do not help QUIC; this shows that on Windows they cost 2.2x. Combined with D-14 keeping bulk on one connection and D-24 quiescing it under media load, the `files` capability should default to a low stream count and treat higher values as a tuning escape hatch rather than the norm. v1.0's eight workers are actively harmful here.

Caveats, stated because they bound what this supports: the two machines differ, and TCP loopback is an imperfect hardware normaliser because it is MTU-sensitive. Loopback measures per-packet CPU cost rather than link behaviour, which is what makes it the right instrument for this question and the wrong one for predicting a real network. **CPU per byte is missing entirely** — see below.

Two harness defects this run exposed, both fixed in the same commit:
- `cpu_sec_per_gib` reported zero on every row. The non-Linux implementation was a stub, while the runbook told the operator the CPU column was the important one. Now implemented with `GetProcessTimes`.
- Results were labelled `gso: "on"` on Windows. The field read the `QUIC_GO_DISABLE_GSO` environment variable on every platform rather than the platform's actual capability, and Windows has no offload to enable. Now reports `"none"` off Linux.

Consequences: ADR-8 keeps its Phase 2 slot (D-32) with the throughput justification weakened and the CPU justification unmeasured. The stream-count finding feeds directly into the `files` capability's defaults. A re-run with the fixed binary would settle whether Windows QUIC is CPU-bound or link-bound at these rates, which is the remaining input to ADR-8's cost/benefit and to PRD R30.

## D-34: Wire schemas written; compiling them found six defects in PROTOCOL.md
Date: 2026-07-30
Status: accepted
Context: `PROTOCOL.md` was written as prose in a single sitting and declared normative. Prose specifications hide ambiguity that a compiler does not, and it was recorded at the time that bugs were expected. D-28 chose `buf` with committed generated code; this entry is the result of actually doing it.
Decision: `proto/openair/v1/` holds eleven schema files — one per capability plus `common.proto` for shared enums — generated into `internal/wire/` and committed. `buf lint` passes STANDARD clean, generated Go compiles and vets, and a round-trip test guards the types. This also creates the root module `github.com/shreyashsri79/openair` decided in D-26; `openair-gui` and `oabench` remain separate modules and are unaffected.

Defects found by compiling, all corrected in the same commit:

1. **`msgType` had no values anywhere.** Section 3 defined the field as "message type within that capability" and nothing ever enumerated it. The envelope was literally unimplementable as specified. Now enumerated per capability — `ControlMessageType`, `FilesMessageType`, and so on.
2. **Enum zero-value collisions.** The spec numbered `Revoke.new_level` from 0 for unpaired and `PathInfo.path_class` from 0 for LAN. proto3 reserves 0 for `UNSPECIFIED`, so every enum is now offset by one and **enum values are not wire values**. That trap is now documented in section 3, the schema README and `common.proto`, because a cast where a conversion belongs would be a silent, protocol-level bug.
3. **`StatRequest` had no response message.** `FileStat` was used as both the data structure and the implied response, leaving the request without a declared counterpart. Added `StatResponse`.
4. **Trust levels were bare `uint32` on two messages with separately implied scales.** `Revoke.new_level` and `CapabilityGrant.level` each carried their own undocumented numbering — which is exactly how a revoke and a grant come to disagree about what level 1 means. Both now use a shared `TrustLevel` ladder.
5. **`Hello.protection_tier` was a bare `uint32`** whose meaning lived only in section 7.3 prose. Now the `ProtectionTier` enum, so tier 3 blocking Owned access is enforceable by type rather than by comment.
6. **`input` having no protobuf messages read as an omission rather than a decision.** It is deliberate — the events are tiny and frequent, protobuf framing would exceed the payload, and a fixed layout demultiplexes without allocating. Now stated as such in section 13 and the schema README.

Consequences: `buf breaking` can now run against the previous commit in CI, which is what mechanically enforces PRD R32's mixed-version compatibility — the ignore-unknown rules in section 3.1 are designed to survive additive change, and `buf` catches the non-additive kind before it ships. Golden vectors for the 8-byte envelope (HLD section 5) are still outstanding; the round-trip test covers the protobuf types only. `mirror.proto` carries a provisional marker matching section 14, pending D-9.

Worth recording for its own sake: every one of the six defects is the kind that survives any amount of proof-reading and dies immediately on contact with a compiler. Spec-first (HLD principle 4) only pays if "the spec" is something a machine can check.

## D-35: The apernet fork is a pseudo-version, and hysteria's BBR must be vendored — corrects D-16
Date: 2026-07-31
Status: accepted (corrects D-16)
Context: D-16 chose BBR from the apernet quic-go fork and stated the plan mechanically: "the dependency therefore becomes apernet/quic-go plus hysteria's BBR", with congestion control installed on a live connection through the fork's exported `congestion` package. Wave 1's X1 was the first attempt to actually resolve those two modules, and neither resolved the way the entry describes.
Decision: Depend on `github.com/apernet/quic-go` at **pseudo-version `v0.60.1-0.20260618182935-599b15a1fa26`**, and **vendor** hysteria's BBR into `internal/congestion/{bbr,common}` rather than importing it.

Two discoveries forced this, both mechanical rather than matters of taste:

1. **Hysteria's BBR cannot be imported.** It lives at `core/v2/`**`internal`**`/congestion/bbr`. Go's internal-package rule makes that unreachable from any module outside `core/v2`, and there is no exported alias. The dependency D-16 describes does not exist; the choice is to copy the code or not to use it.
2. **The fork's release tags are unusable under the fork's own import path.** apernet publish tags mirroring upstream (`v0.61.0` and so on) whose `go.mod` still reads `module github.com/quic-go/quic-go`, so requiring one under `github.com/apernet/quic-go` fails outright. Only their branch head declares the apernet module path, and only it carries the exported `congestion/` and `monotime/` packages. The pseudo-version *is* the release. Hysteria v2.10.0 pins the same commit.

What D-16 got right survives intact and is the reason this is cheap: `(*quic.Conn).SetCongestionControl` is public API on the fork, so BBR is installed on a live connection with no patch against quic-go's `internal/`. D-14's vendoring cost — re-merging a local patch on every upstream security release — remains avoided.

Alternatives considered: *Reimplement BBR* — rejected, this is precisely the subtle-correctness work the build plan forbids delegating and would be worse done twice. *Import upstream quic-go and give up BBR* — rejected, it discards D-14 and D-16 entirely. *`replace github.com/quic-go/quic-go => github.com/apernet/quic-go v0.61.0`* — tried first, and it fails: the tagged tree has no `congestion/` package to install through, so the replace directive buys a fork with none of the reason for wanting one.

Consequences: `internal/congestion/PROVENANCE.md` records the upstream, the version, the exact two local modifications (one import path, one debug environment variable) and the re-sync procedure, so that updating stays a diff rather than an archaeology exercise. Upstream's own `bbr_sender_test.go` is kept and passes, which is the check that the port is faithful. `golang.org/x/exp` enters the module for this vendored code alone. The maintenance risk D-16 named is now sharper rather than softer: the project tracks a branch head, not a tag, and anyone bumping it must confirm `congestion/` still exists in the target before doing so. A fork that stops exporting it silently removes the ability to install any congestion controller at all.

## D-36: BBR runs the conservative gain profile by default
Date: 2026-07-31
Status: accepted
Context: D-16 identified BBRv1's standing queue — roughly 2×BDP inflight, plus ProbeRTT dips — as the one v1 defect that genuinely bites this architecture, because D-14 keeps bulk, clipboard, input and eventually `mirror` datagrams on a single connection sharing a single congestion controller and pacer. It named the mitigation as "a knob rather than a rewrite: one constructor using `highGain: 2.25` against the `2.885` default". Vendoring the sender (D-35) made that knob available as a named profile.
Decision: `internal/congestion.DefaultProfile` is `bbr.ProfileConservative` — `highGain` 2.25, `highCwndGain` 1.75, drain-to-target and overshoot detection on — and `congestion.Use` installs it. Both `conn.Dialer.DialAddr` and `conn.Listener.Accept` call it once the handshake completes, which are the only two places a connection is established.

Alternatives considered: *`ProfileStandard`, upstream's default* — rejected as the default because it optimises the quantity this architecture can most afford to lose. *`ProfileAggressive`* — rejected outright; it raises `highGain` to 3.0 on a connection that also carries keystrokes. *Per-capability tuning* — not possible, and that is the point: one connection means one controller, which is why the default matters so much.

Consequences: peak bulk throughput is traded for a shorter bottleneck queue. That is the right side of the trade because HLD's sub-50 ms glass-to-glass target and its requirement that input never queue behind bulk are far harder to recover than the throughput given up, and D-17 already showed the shared connection beating separate connections for interactive latency by a factor of seven. `TestDefaultProfileIsLatencyFirst` fails deliberately if the constant is flipped back, because a silent reversion would show up only as latency regressing under load — the exact failure mode D-16 warned about. The profile is a parameter, so a future bulk-only path with no interactive traffic may pass `ProfileStandard` explicitly. D-17's Cubic baseline is still the number BBRv1 must be measured against; that comparison remains outstanding.

## D-37: Inbound peers are authorised by an explicit callback on the session
Date: 2026-07-31
Status: accepted
Context: M1a reported, and inspection confirmed, that nothing authorised an inbound peer. `session.New` compares the peer's TLS key against `Config.Peer`, but that check only fires when `Peer` is populated — which the dialling side can do and a listener structurally cannot, because it does not know who is calling until Hello arrives. `conn/listener.go` carried a comment saying authorisation "happens inside `session.New`"; `session.Config`'s own comment said `New` "leaves authorisation to the caller". Two comments claiming the opposite thing, and between them a listener that admitted every caller unconditionally.
Decision: `session.Config` gains `Authorize func(identity.Peer) error`, invoked once Hello has populated the peer record — DeviceID and identity key derived from the TLS certificate, display name and protection tier as claimed — and **before** `startQueues` makes capability dispatch possible. Returning an error closes the session. `conn.Listen` takes the callback and passes it through.

A nil `Authorize` admits any peer. That is deliberate and it is correct only for M1, whose scope is an explicit-address dial with the fingerprint shown and accepted interactively. M2 replaces the callback body with a trust-store lookup, at which point nil must stop being acceptable on the listening path.

Alternatives considered: *Give `session.Config` a `TrustStore`* — rejected for M1, which has no trust store to consult and would have to stub one; it also pushes a policy decision into the layer that should only enforce it. *Authorise in `conn` after `New` returns* — rejected, because by then the control loop is running and a capability message may already have been dispatched. The gate has to be inside the handshake, not after it.

Consequences: the hole is now a named, typed thing that a reader can find, rather than an accident living in the gap between two contradictory comments. Two regression tests over real QUIC assert that a rejecting callback yields no session and that the callback sees the peer's real DeviceID — derived from the TLS key, not taken from Hello, so a lying peer cannot influence what the gate is shown. This is a Phase 1 seam: M2 changes the callback's body, not its shape.

## D-38: An unknown envelope version closes with PROTOCOL_VIOLATION — PROTOCOL.md §3 and §10 disagree
Date: 2026-07-31
Status: accepted (records a defect in PROTOCOL.md; the spec still needs the edit)
Context: §3 states that a receiver seeing an unknown `ver` MUST close the connection with `PROTOCOL_VIOLATION` (0x01). §10's error table defines `0x02 UNKNOWN_VERSION`, "Unsupported envelope version" — a code whose only possible trigger is the condition §3 has just assigned to a different code. Two wave-1 workers hit this independently, from opposite directions: M1a implementing the decoder, and X4 deriving golden vectors by hand from §3. Both resolved it identically without conferring.
Decision: follow §3's literal text. An unknown `ver` produces `CodeProtocolViolation`. `UNKNOWN_VERSION` remains defined but unreachable. The golden vectors pin this behaviour, so implementation and vectors agree.

Alternatives considered: *Follow §10 and use `UNKNOWN_VERSION`* — the more specific code and arguably the better design, but §3 is the section that actually specifies decoder behaviour and it is unambiguous. Changing behaviour to match a table entry would mean shipping something the normative prose forbids.

Consequences: **`PROTOCOL.md` still needs an edit** — either §3 names `UNKNOWN_VERSION`, or §10 deletes it as unreachable. Until then the spec contradicts itself and the next implementer will hit this a third time. The code and the vectors are consistent today, so an edit is not urgent, but it is real. Worth noting that hand-derived golden vectors caught this: it is the argument for X4's discipline of deriving expected bytes from prose rather than from the implementation.

## D-39: Not every enum is offset by one, and ProtectionTier's two scales run in opposite directions — corrects D-34
Date: 2026-07-31
Status: accepted (corrects D-34)
Context: D-34 established that proto3 reserves 0, so wire values differ from generated enum values, and stated the rule as "every enum in the schemas is offset by one". §3 repeats it. Implementing the conversions showed the rule is wrong in both directions, and the second case is a live hazard rather than a documentation nit.
Decision: record the two exceptions explicitly, and convert through a table in every case rather than reasoning about offsets.

1. **`msgType` is not offset.** PROTOCOL.md never enumerated message types — that was D-34's own defect 1 — so the schemas are the original definition and the generated enum value *is* the wire value. §3's prose reads as though the offset covers `msgType`; the sentence that follows lists only capID, `TrustLevel`, `ProtectionTier`, `ConsentScope` and `PathClass`. The truth is currently inferable only from an absence in a list, which is how the next person gets it wrong.
2. **`ProtectionTier` is not offset either, and its two scales are inverted.** §7.3 numbers the tiers 1/2/3 and the schema matches, so there is no offset. But `identity.ProtectionTier` numbers them 0/1/2 **in the opposite order**, with `TierNone` first. A cast rather than a conversion therefore turns keystore-backed into none — it silently downgrades a TPM-protected peer to "unprotected", which is exactly the direction that matters, since D-21 forbids granting Owned to a tier-none device.

Consequences: `internal/session/convert.go` holds every wire↔domain conversion in one file, and `TestProtectionTierIsNotOffset` fails deliberately if the two scales ever coincidentally line up — a test that exists to break when the hazard stops being visible. **`PROTOCOL.md` §3 needs an edit** stating plainly that `msgType` is not offset, rather than leaving it to be inferred. D-34's blanket phrasing should be read as superseded on this point.

## D-40: A chunk offset is transfer-global across the offered files concatenated in offer order
Date: 2026-07-31
Status: accepted (records a gap in PROTOCOL.md §8.3; the spec needs the amendment)
Context: §8.3's chunk frame is `offset(u64) + size(u32)` with no file identifier, while §8.1 offers `repeated FileMeta`. Nothing in the spec says what `offset` is relative to, so a multi-file transfer is unaddressable as specified. In parallel, `ChunkManifest.chunk_sha256` and `TransferAccept.have_chunks` are both indexed by a "chunk index" the spec never defines.
Decision: `offset` is a **transfer-global offset into the offered files concatenated in offer order**, and **a chunk never spans a file boundary** — so the last chunk of each file is short, and chunk index maps to exactly one file. This resolves the manifest and resume indexing at the same time, because a chunk index is now unambiguous.

Alternatives considered: *Add a file index to the frame header* — the cleaner design, but it changes a 12-byte header inherited byte-for-byte from v1.0 (D-4) and would cost a wire break for something the concatenation model already solves. *Let chunks span file boundaries* — rejected: it makes a chunk index correspond to a byte range in two files, which breaks per-chunk verification and makes resume state far harder to reason about.

Consequences: **this is wire-visible and any other implementation must match it, so `PROTOCOL.md` §8.3 needs the amendment.** It is the highest-priority spec edit out of wave 1, because unlike D-38 and D-39 it is not a documentation clarification — two implementations that choose differently will corrupt files rather than fail to connect. Short trailing chunks are now normal rather than exceptional, which the plan and its coverage test account for.

## D-41: The session dispatches inbound messages through one bounded queue per capability
Date: 2026-07-31
Status: accepted
Context: the control loop reads envelopes off a single stream and must hand them to capabilities. How it does so determines whether a capability can perform a request/response round trip, whether message ordering survives, and what happens when a capability stops keeping up.
Decision: one goroutine and one bounded channel (depth 64) per negotiated capID.

Alternatives considered: *Synchronous dispatch on the read loop* — rejected, it deadlocks any capability that sends a message and waits for the reply, because the reply cannot be read while the handler is still on the stack. *One goroutine per message* — rejected, it loses ordering, which offer/cancel sequences depend on: a cancel overtaking its offer is a transfer that never stops.

Consequences: a wedged capability eventually fills its queue and blocks the control loop for every capability, at depth 64. That was taken as the honest failure mode over the alternative of dropping messages, which would turn a stalled capability into silent protocol corruption elsewhere. It does mean one badly-behaved capability can stall a session, so this bound is worth revisiting when a capability with genuinely bursty control traffic exists — `mirror` is the likely first.

## D-42: The privilege public key lives in a sidecar file until Appendix A carries it
Date: 2026-07-31
Status: accepted (interim; records a defect in PROTOCOL.md Appendix A)
Context: a device must know its own privilege **public** key while the private half stays sealed — §5.2 sends it during pairing and the UI displays it. Appendix A's at-rest container has no field for it. As specified, obtaining the public key requires unsealing the private one, which contradicts D-20's premise that a locked device stays useful and reachable.
Decision: store it in a sibling `privilege.pub` file, and cross-check it against the sealed key on every unseal so that substituting the sidecar is detected rather than trusted.

Consequences: the sidecar is unauthenticated on its own, which is why the cross-check exists — but the check only runs at unseal time, so a substituted sidecar is caught late rather than never. The proper fix is an **Appendix A version 2 carrying `public_key` (32 bytes) inside the authenticated header**, which removes the sidecar and the unauthenticated-file problem together, and that edit is recommended before M6 builds the unlock flow on top of this container. Two smaller Appendix A gaps found alongside it: it specifies no integer endianness (it is labelled "normative, not wire", so §0's little-endian rule arguably does not reach it — §0 was applied), and `ct_len` is redundant with the file length (both are validated to agree rather than either being trusted).

## D-43: A threat model exists, and it records four unresolved security questions
Date: 2026-07-31
Status: accepted (the document); the questions inside it are open
Context: PRD R29 asks for a threat model. The security reasoning existed but was scattered across the decision log and PROTOCOL.md, where no reader could evaluate it as a whole.
Decision: `docs/threat-model.md` assembles it — assets, five trust boundaries, eight named adversaries, what rendezvous and relay operators each learn, accepted weaknesses, and non-goals cross-referenced to Appendix C. Every claim carries an inline section or decision citation. R29 is satisfied as a documentation requirement.

The value is in what assembly exposed. Four items need a maintainer decision rather than a note:

1. **D-20 and D-21 conflict.** D-21 claims tier 1 "closes the cold threat completely". D-20 has `auth_policy = never` devices auto-unsealing from TPM boot state with no human present. A PCR-only policy releases the key to whoever boots the stolen hardware — and that is the always-on desktop, the device holding the most valuable Owned access. Same shape as TPM-sealed disk encryption with no PIN.
2. **The trust store has no specified at-rest integrity, anywhere.** Appendix A covers the privilege key only. Write access to the trust store file inserts an attacker's own public key as an Owned peer, or flips a level, or rewrites a protection tier — and every check in §6 and §7.3 then passes honestly. It is cheaper than attacking any cryptography above it. (M1b's trust store enforces two invariants at the storage layer — a record's DeviceID must derive from its own identity key, and `LevelOwned` is refused for a tier-none peer — which raises the cost but is not integrity protection.)
3. **`RelayAuth` has no domain separation and no binding to the relay's identity** (§17). It signs over both nonces, unlike §5.2, §6 and §16, which all carry a prefix. A hostile relay can proxy another relay's challenge and claim the client's mailbox there. This is a straightforward spec defect with a straightforward fix.
4. **DeviceID is a permanent tracking identifier.** mDNS broadcasts it in clear on every network joined (§15), `LookupRequest` is unauthenticated (§16), and it never rotates (§2). Composed, that is a stable identifier resolvable to a current IP by anyone on any LAN the device visits.

Consequences: items 1 and 2 are maintainer calls, 3 is a PROTOCOL.md edit, 4 is a design question that becomes harder to change the longer DeviceID is load-bearing. Also recorded: D-18 says to store an Argon2id *hash* of the credential while D-19 and Appendix A derive `K_master` and verify via the AEAD tag. Appendix A is right, but D-18 was never superseded on the point, so a reader following the log in order builds the weaker thing — treat D-18 as superseded by D-19 on credential storage.

## D-44: CI enforces the definition of done; the netem matrix stays manual
Date: 2026-07-31
Status: accepted
Context: the build plan's definition of done names five conditions per milestone, three of them mechanical. Nothing enforced them.
Decision: three workflows. `ci.yml` runs `gofmt -l`, then `go build`, `go vet`, `go test -race` and `GOOS=windows go build` across both Go modules (root and `oabench`, which have different Go versions and so are pinned per-module from their own `go.mod`). `buf.yml` runs `buf lint` and `buf breaking`. `netem.yml` is **`workflow_dispatch` only, not scheduled**.

The netem decision is the considered one. `oabench/netem/lab.sh` uses `unshare -Urn` specifically to avoid needing root, but Ubuntu's AppArmor policy since 23.10 blocks unprivileged user-namespace creation by default, so it fails out of the box on `ubuntu-latest`. The sysctl workaround is included along with a preflight step that fails loudly if it did not work, but none of it can be verified without actually running on a hosted runner. A nightly job that is red every night trains everyone to ignore CI, which is worse than no job.

Consequences: `GOOS=windows go build` is now enforced on every push, which is what D-32 needs to keep Windows from rotting silently while its milestone is deferred. Three gaps CI cannot close, recorded rather than papered over: the definition of done's first condition ("runs end to end, by hand, on two real endpoints") is hardware-in-the-loop and no job substitutes for it; `openair-gui` is a third module needing system GL/X11 packages and is currently ungated; and `buf generate` reproducibility — that committed `internal/wire/` still matches what codegen produces — is checked by nothing, though `git diff --exit-code` after a generate step would catch it.

## D-45: PROTOCOL.md §5.2 does not fully specify the pairing transcript; three gaps filled the same way on both sides
Date: 2026-07-31
Status: accepted

Context: §5.2 defines the short authentication string as `SAS = decimal(SHA-256(transcript)[0:4]) mod 1000000` over a transcript of a domain separator, both identity keys, both privilege keys and both nonces. The SAS is the entire security of pairing — TLS is unauthenticated at that point, and two devices that derive different digits from the same exchange fail closed but never pair. Implementing M2 found three places where two conforming implementations could disagree.

Decision: fill all three in `internal/pairing/sas.go`, and record here what the spec has to say for anyone else to interoperate.

1. **Both key pairs are sorted by value, not by role.** `min(idA,idB) || max(idA,idB)`, likewise for the privilege keys. This is what makes the digits role-independent: neither device has to agree with the other about who is "first", so both derive the same bytes from the same facts. Sorting by role instead would need a tie-break the spec does not give.
2. **A device at protection tier none has no privilege key (D-21), and §5.2 gives no encoding for its absence.** Omitting the field would make the transcript variable-width with no delimiters, so the two sides could parse the same bytes differently. It is encoded as 32 zero bytes, keeping every field fixed-width.
3. **The nonces are ordered by role, and "which role" was ambiguous.** §5.2 glosses the order as "initiator's nonce first", but §5.1 has the *offerer* initiate the pairing while the *scanner* initiates the connection. The literal `nonceA || nonceB` with A as §5.1 defines it — the device that displayed the code — is what is implemented.

Alternatives considered: ordering the nonces by their owner's identity key, the way the keys are ordered (rejected for now — it would remove the last role dependence and the ambiguity with it, which is strictly better, but it is a wire-visible change to a normative section and belongs in a spec edit rather than in an implementation choice made unilaterally). Treating the absent privilege key as an error and refusing to pair with a tier-none device (rejected — D-21 exists precisely so those devices can still reach Trusted).

Consequences: **`PROTOCOL.md` §5.2 needs three edits** — state the sort, state the zero-byte substitution, and disambiguate the nonce order (preferably by adopting the sort there too). Until then this implementation is the de facto definition, which is the situation D-34 already flagged as the cost of a prose spec. A fourth, smaller edit belongs with them: **§6.4's prose still describes its own trust scale ("1 = Trusted, 2 = Owned")** while the schema types `CapabilityGrant.level` as the shared `TrustLevel` enum. That is exactly the disagreement between a grant and a revoke that D-34's defect 4 introduced `TrustLevel` to prevent; the schema is right and the prose is stale.

## D-46: A message that ends the conversation lingers before the session closes
Date: 2026-07-31
Status: accepted

Context: two failures in M2, found by tests rather than by reading. A declined `PairConfirm` never reached the peer, which then sat waiting out the full two-minute pairing timeout while the user who declined had already walked away. A `Revoke` carrying `new_level = 0` never reached the peer either — and that one is worse, because §6.1 requires the peer to discard both pinned keys on receipt, so it went on believing a pairing that had been destroyed still held.

Both have one cause: QUIC's `CONNECTION_CLOSE` preempts stream data that has not been flushed. Writing a message and closing immediately behind it loses the message. `oabench` hit the same thing and worked around it by draining the control stream to EOF before closing (recorded in the functionality map), so this is the second independent encounter with it.

Decision: a message that ends the conversation waits `notifyLinger` — 250 ms — before the close that follows it. It applies in exactly two places, both in `internal/pairing`: `settle` after a declined `PairConfirm`, and `Revoke` after an unpairing `Revoke`.

Alternatives considered: draining the control stream to EOF before closing, as `oabench` does (rejected here — the peer is not obliged to close its side promptly, so the drain has no bound; the same reasoning does not apply to `oabench`, where both ends are the same program). Not closing at all and letting the peer close on receipt (rejected as the sole mechanism — it is the normal path and does happen, but a peer that ignores the message would leave the session open indefinitely; the local close is the fallback). Making enforcement depend on delivery (rejected outright — §6.3 already establishes that enforcement is local and `SessionKill` is a courtesy, which is why the trust store and the live `Guard` are both updated *before* the message goes out; the linger only decides whether the peer is *told*, never whether the revoke took effect).

Consequences: `Revoke` blocks its caller for a quarter of a second on a deliberate user action, which is not worth engineering around. The real limitation is that 250 ms is a heuristic, not a guarantee — a peer on a slow path can still miss the notification, and the design has to stay correct when it does. It is: the revoking side has already deleted the record and lowered the guard, so a peer that never hears about it is refused on its next operation regardless. **A general fix belongs in the session layer, not here** — something like a `CloseAfterFlush` on `session.Session` that waits for the control stream's write side to be acknowledged. M4's daemon will want it, since every capability that ends a conversation has this problem.

## D-47: The unicast discovery fallback needed a byte layout, and PROTOCOL.md §15.2 does not give one
Date: 2026-07-31
Status: accepted

Context: §15.1 specifies mDNS discovery precisely — service type `_openair._udp`, four TXT keys, `id` / `v` / `port` / `n`. §15.2 says only that where multicast is unavailable a peer MAY fall back to unicast probes of known-last-good addresses and MAY broadcast an announce. That is a description of intent, not a format: two implementations reading it would not interoperate, because there is nothing to agree on.

Decision: define it, in `internal/discovery/announce.go`, and write it back into §15.2.

```
 0       4     5     6
+-------+-----+-----+------------------------------+
| "OA2D"| ver | typ | n * (uint8 len, len bytes)   |
+-------+-----+-----+------------------------------+
```

Body is the same `key=value` strings the TXT record carries, length-prefixed rather than newline-separated because a display name may contain anything. Two types: an announce, and a query carrying only the asker's DeviceID. Port 53318, deliberately **not** 5353 — the fallback exists for networks that filter multicast, and reusing the mDNS port puts it behind the same filter.

Three properties worth stating, because each was a choice:

- **A query is answered unicast to the asker, not broadcast.** Nobody else asked, and it makes discovery converge in one round trip instead of one beacon interval — which is most of PRD R6's three-second budget.
- **The self-filter is by DeviceID, not by comparing addresses against this host's interfaces** (v1.0's approach). That is what lets two instances on one machine see each other, which v1.0 could not represent and every test in this package depends on.
- **Every length is checked before it is used to slice.** This reads from an unauthenticated UDP socket that anyone on the network can write to, so a datagram claiming a 200-byte field in a 10-byte packet is rejected rather than panicking. Unparseable datagrams are dropped silently: logging each one would be a log amplification vector.

Alternatives considered: reusing protobuf, as the session layer does (rejected — this is four short strings on a socket that has no session and no negotiated version, and adding a schema dependency to reach it buys nothing). Broadcasting the answer to a query (rejected — it doubles traffic on every network where it works and tells devices that did not ask). Omitting the query and relying on periodic announces alone (rejected — a device that starts between two beacons waits a full interval, which spends R6's budget on nothing).

Consequences: **§15.2 needs this written into it** before any second implementation exists — it is wire-visible, and the current text cannot be implemented twice compatibly. `_openair._udp` also means a v1.0 device on `_tcp` and a v2 device are mutually invisible, which is intended: they share no wire protocol, so seeing each other could only produce a failed connection.

## D-48: A process that is not accepting sessions browses without announcing
Date: 2026-07-31
Status: accepted

Context: `openair send laptop` and `openair discover` both need to hear what is on the network, and neither is listening for inbound sessions. The discovery config as first written required a port, because an announce without one is meaningless — so a browsing process would have had to invent a port and publish it.

Decision: `Config.BrowseOnly`. It sends queries and listens, never announces, never answers a query, and does not require a port. `recv` announces the port it actually bound; `send` and `discover` browse.

Rationale: announcing a port this process is not listening on publishes an address that refuses every connection made to it. Peers would show the device in their list, dial it, and fail — and the failure would look like a network problem rather than a lie. A device that cannot be connected to should not appear connectable.

Alternatives considered: announcing the default port 9000 regardless (rejected for the reason above; it is only correct by accident, when a daemon happens to be listening on the same machine). Having `send` skip discovery entirely and require an address (rejected — it is exactly the typing M3 exists to remove). Announcing with a zero port and letting peers filter (rejected — it pushes a rule into every consumer instead of not saying the thing).

Consequences: `recv` advertises the bound port rather than the requested one, so `--listen :0` works and does not tell peers to dial port zero. When M4's daemon arrives it is the only listening process, so it becomes the only announcer, and the CLI browsing beside it stays correct with no change.

## D-49: The Android shell is a second launcher entry over a root-level `mobile/` façade, and the .aar is build output
Date: 2026-07-31
Status: accepted (implements ADR-5 / D-31)

Context: D-31 verified that gomobile binds the v2 core. It did not say where the bindable package lives, how the .aar reaches Gradle, or what happens to the working v1.0 Kotlin app while the v2 shell is incomplete.

Decision, in three parts.

**The façade lives at `mobile/`, at the repo root.** `gomobile bind` cannot bind `internal/...`, so the binding has to sit outside the internal tree — which means the one package in this repo that is importable by anyone is also the one that has to stay inside gobind's subset of Go. That subset is narrow enough to shape the API: no unsigned integers (the core counts bytes in `uint64`, converted at the boundary), no slices but `[]byte` (so `DeviceList` and `FileList` are objects with index accessors), no maps, no channels, no generics, callbacks as Java-implemented interfaces. `doc.go` states it, because every one of those is a `gomobile bind` failure with an unhelpful message when it is forgotten.

**The .aar is a build artifact and is not in version control.** `openair-android/build-aar.sh` produces `app/libs/openair.aar`, and Gradle picks it up through a `fileTree` so the project still configures on a fresh clone — only the v2 shell fails to compile until the binding has been built. It is roughly 7 MB per ABI, rebuilt from `mobile/` and `internal/` on every change, which is exactly what AGENTS.md §6 keeps out of the tree.

**v1 and v2 are two launcher entries, not one blended UI.** They share no wire protocol: v2 is QUIC announcing `_openair._udp`, v1 is TCP on `_openair._tcp`, so a v1 device and a v2 device are mutually invisible by construction. Merging the screens would produce a device list where half the entries silently cannot be transferred to. The v1 screen is removed when the v2 shell reaches parity, not before.

Alternatives considered: putting the façade under `internal/` and binding a thin wrapper elsewhere (rejected — the wrapper would be the façade, one indirection later). Committing the .aar so a clone builds without Go (rejected — it is 7 MB per ABI of derived output that goes stale silently against the Go tree it was built from). Replacing the v1 UI outright (rejected — it works today and v2 does not do notifications, clipboard or unlock yet; deleting a working app to make room for an incomplete one is a regression the user experiences).

Consequences: the binding's threading contract is now load-bearing in the shell. Blocking calls (`SendFiles`, `AwaitPeer`, `PairWithOffer`) occupy their thread for the whole operation and must not run on the main looper; the two synchronous prompts (`SASVerifier`, `OfferVerifier`) are called on a Go goroutine and hold the exchange open until they return, which the view model bridges through a `CompletableDeferred`. Discovery is polled rather than subscribed, because gobind cannot carry a channel across the boundary — 500 ms, which is what a device picker wants anyway.

Verified on this machine: `gomobile bind` produces a 7.5 MB arm64 .aar in about 50 seconds, the Kotlin compiles against it, and `assembleDebug` packages `lib/arm64-v8a/libgojni.so` into the APK. **Not verified: anything on a real device.** No handset was attached, so on-device transfer, battery and throughput remain what D-32 and the `oabench/androidkit/` runbook still owe.

## D-50: ADR-8 is implemented in a local fork checkout — unpublished, and unmeasured
Date: 2026-07-31
Status: accepted as a statement of where the work is · corrects D-22's constants note and D-27's fork base

Context: X2 is the Windows fast path — USO on send, URO on receive — that D-22 and D-23 committed the project to and D-32 deferred to Phase 2. The code is now written. Three things about it disagree with what the decision log already says, and the deferral means none of it is finished.

**The fork base is `apernet/quic-go`, not upstream `quic-go/quic-go`.** D-27 specifies `replace github.com/quic-go/quic-go => github.com/shreyashsri79/quic-go v0.61.0-openair.1`. That predates D-35, which moved the dependency to `github.com/apernet/quic-go` at pseudo-version `v0.60.1-0.20260618182935-599b15a1fa26` because that fork exports the congestion API BBR needs. `go.mod` today carries no `replace` at all. So the patches are written against the apernet tree, and D-27's line is stale in the module it names — its mechanism (a fork repo consumed by `replace`, rebased rather than merged, patches kept as distinct upstreamable commits) still stands.

**`x/sys/windows` already defines the constants D-22 said would need declaring.** As of v0.45.0 it has `UDP_SEND_MSG_SIZE`, `UDP_RECV_MAX_COALESCED_SIZE`, `UDP_COALESCED_INFO`, `WSAMsg` and `WSACMSGHDR`. What it does not provide is the `WSA_CMSG_*` alignment macros, so the control-message walk is written out by hand — header length rounded to a pointer boundary, every length checked against what is actually present before it is used to slice, because the receive loop must not panic on a malformed control message.

**The sticky socket option is not free, and D-22 treated it as though it were.** D-22 chose `UDP_SEND_MSG_SIZE` as "a sticky socket option rather than a per-send cmsg" as if that were merely a different spelling of Linux's `UDP_SEGMENT`. It is not. The option belongs to the socket, and one socket carries every connection a server has, so two connections sending concurrently can interleave as "A sets 1200, B sets 1400, A sends" — and A's entire batch is segmented at B's size. Linux has no such problem because the segment size travels with the data. The fix is a mutex covering the set and the send together, which serialises sends on the socket; the size is cached so a run of same-sized packets costs no extra syscall, but the lock is a real cost that the Linux path does not pay and that ADR-8's cost/benefit did not account for.

Decision: the work lives in a **local fork checkout at `~/Work/quic-go-openair`**, on top of `599b15a1`, as two commits — `6f3d3814` (classify Windows send-offload errors so `sconn.Write` falls back) and `b967fb9d` (the `winConn` implementing both offloads). Nothing is published: no fork repository was created, nothing was pushed, and **no `replace` pointing at a filesystem path was committed to this repo**, because that would break the build and CI for every other checkout. Publishing the fork is a maintainer action.

Verified: both patches build and vet for `GOOS=windows`, every other platform still builds, quic-go's own tests pass on Linux, and OpenAir itself cross-compiles for Windows against the patched fork with a transient `replace` that was then reverted.

**Not verified, and this is the part that matters:** no Windows machine was involved. The new tests have never been executed — they are `//go:build windows` and were only type-checked. Nothing has measured whether the offload actually engages, and D-22 required a before-and-after number precisely so that "we believe it should be faster" could not stand in for a result. D-33's baseline (1450 Mb/s at one stream, 647 at four) is the "before"; the "after" does not exist yet. Until it does, ADR-8 is written, not done, and D-32's Phase 2 exit blocker for Windows is unchanged.

Consequences: the immediate follow-up is a Windows session — run the fork's tests, then `oabench/winkit/`'s runbook against a patched build, and record the pair. Two things are worth watching when that happens. The 2.2x collapse from one stream to four (D-33 finding 2) is unexplained and may not be a send-path problem at all, in which case USO will not touch it. And the mutex above means a server socket shared by many connections now serialises its sends, so a multi-connection Windows server is the case most likely to regress rather than improve.

## D-51: Daemon IPC is capID 7, and every daemon message carries `request_id` as field 1
Date: 2026-07-31
Status: accepted (implements D-29)
Context: D-29 chose the session envelope over gRPC for the link between `openaird` and the shells that drive it, and left two things unstated that an implementation cannot avoid deciding. Which capID does a local-only control plane use, given that Appendix B's table is a *network* registry? And how is a reply matched to its request, given that the envelope has no correlation field and gRPC was the thing that would have supplied one?
Decision: **capID 7**, added to Appendix B and to `CapabilityId` as `CAPABILITY_ID_DAEMON`, marked local-only. And **`request_id` as field 1 of every message in `daemon.proto`**, allocated by whichever side initiates, echoed by the responder.
Rationale for the capID: the alternative was the 128–255 experimental range, which Appendix B says must not appear in a release — and this will. Taking a core ID reserves it against a future network capability colliding with it, which is exactly what the reserved range is for. It costs nothing on the network: a peer sending capID 7 over QUIC reaches no registered handler and is ignored under §3.1.
Rationale for field 1: it makes the ID readable without unmarshalling. proto3 encodes field 1 first, so a set request ID is always tag `0x08` followed by a varint at the head of the payload, and the read loop can route a reply to its waiting caller without knowing which of fifteen message types it is holding. That removes a type switch from the hot path and, more importantly, removes the need for every message to implement a Go interface. `TestEveryDaemonMessageHasRequestIDFirst` enforces the invariant against the schema itself, because a future message numbering it differently would route every reply to request 0 and hang the caller rather than fail visibly.
Alternatives considered: *a wrapper message with a oneof body* (rejected — it would make the local link's framing differ from the network's, which is the one thing D-29 chose this design to avoid). *A correlation ID in the envelope header* (rejected outright — the envelope is wire-visible and version-frozen; adding a field to it for the benefit of a local socket would change the network format for nothing).
Consequences: two ID namespaces exist, because prompts travel daemon-to-client while requests travel client-to-daemon and both counters start at 1. They are kept in separate maps, keyed off the message type, and `TestPromptsAndRequestsDoNotCollide` is the proof. `DaemonDevice` has no `request_id` and is exempt from the invariant: it is a nested element, never an envelope payload.

## D-52: An inbound Hello runs off the accept path, and a refused peer does not end the accept loop
Date: 2026-07-31
Status: accepted
Context: `conn.Listener.Accept` ran the Hello exchange inline. The functionality map has recorded since M1 that this lets one peer which completes the QUIC handshake and then says nothing hold up the accept loop until the idle timeout, and that the daemon would need a real fix. The CLI worked around it by accepting in a background goroutine, which moves the stall rather than removing it. A second defect surfaced while wiring the daemon: `Accept` returned a rejected peer's error to its caller, and every caller treated an error as terminal — so a stranger connecting to a listening device stopped it listening.
Decision: the listener owns a pump goroutine. It accepts QUIC connections and runs each Hello on its own goroutine, bounded at 32 in flight with a 10 s deadline each, delivering results to `Accept` through a channel. A handshake that fails is delivered as a `*conn.HandshakeError`, which the caller is expected to log and continue past; only a listener-level failure is terminal.
Rationale: both failures are denial of service by anyone who can reach the port, and neither needs an attacker — a peer on a bad network produces the first and an unpaired device produces the second. The bound is a memory limit rather than a throughput one: each pending handshake holds a connection and a goroutine, and without a cap an attacker accumulates both.
Alternatives considered: *unbounded handshake goroutines* (rejected — it trades a stall for an allocation attack). *Swallowing handshake failures inside the listener* (rejected — `internal/conn`'s own tests assert that a peer refused by the authorize callback surfaces as an error, and a listener that silently drops refusals gives an operator no way to see one).
Consequences: `session.Session` gained `Done()`, closed when a session ends however it ended. The daemon needs it to drop dead sessions from its device list; without it the only way to notice is to send something and watch it fail, which turns every stale entry into a failed user action. `Accept` now returns `conn.ErrListenerClosed` after `Close`, rather than racing the pump's own error.

## D-53: With nobody watching, an unattended daemon refuses
Date: 2026-07-31
Status: accepted
Context: `openaird` receives files with no terminal attached. §8.1's accept decision and §5.2's six digits both need a human, and the daemon may have none: no tray UI, no `openair watch`, nothing subscribed to its socket.
Decision: the daemon asks every subscribed client that offered to answer prompts and takes the **first answer**. No such client, or no answer within the timeout — 60 s for a transfer, 2 minutes for pairing — is a **refusal**. Accepting without a human requires `--accept-all`, stated at start-up.
Rationale: the failure has to be the safe one. A daemon that accepted a transfer because nobody was looking would write a stranger's files to disk on the strength of an unattended socket; a daemon that refuses one costs the user a retry with `openair watch` running, and `openair status` says exactly that when it is the case. First answer rather than unanimity, because two open UIs are two views of one user and requiring agreement would hang on whichever one nobody is looking at.
Alternatives considered: *queue the prompt until someone connects* (rejected — the peer is waiting on the other side of §8.2's accept, and a transfer that hangs for an hour is worse for both ends than one that is refused now). *Accept from Owned peers unattended* (deferred, not rejected — that is M6's unlock token and it does not exist yet; `files.Config`'s nil-Accept default already encodes it for when it does).
Consequences: `--accept-all` is the headless posture and it is a real widening — any paired device may then write into the destination directory without asking. It is the honest way to express what a headless install is, and it is visible in `openair status` rather than implicit.

## D-54: The system clipboard is reached by exec'ing the desktop's own helper
Date: 2026-07-31
Status: accepted
Context: M5's `clipboard` capability (§9) is pure Go and needs no platform code. Putting content *into* a clipboard does: X11 and Wayland have no in-process API without a binding, Windows needs the clipboard API or PowerShell, macOS has `pbcopy`.
Decision: exec the helper the user's desktop already ships — `wl-copy`/`wl-paste` first on Linux, then `xclip`, then `xsel`; `pbcopy`/`pbpaste` on macOS; `Set-Clipboard`/`Get-Clipboard` via PowerShell on Windows. Wayland is tried before X because on a Wayland session `xclip` talks to XWayland and reaches a clipboard half the applications cannot see. Absence of any helper is `ErrNoClipboard`, a normal condition and not a failure: a headless daemon still accepts pushes and reports them as events, because whether this machine has somewhere to paste is not the sender's problem.
Alternatives considered: *a cgo X11/Wayland binding* (rejected — it puts a display dependency into a daemon that mostly runs without one, and the `GOOS=windows` build gate from D-32 would have to carry it too). *A pure-Go X11 client* (rejected — it solves one of three platforms and still needs a Wayland path).
Consequences, and the one that cost an afternoon: **`wl-copy` forks a child that holds the selection until something replaces it.** Give the command an `os/exec` pipe for stderr — which is what assigning a `bytes.Buffer` does — and the forked child inherits it, so `cmd.Run` blocks until the pipe reaches EOF, which is to say until the user copies something else. The daemon's receive path hung on exactly this, silently, with no error anywhere. The fix is a real file rather than a pipe, plus `cmd.WaitDelay`. The OS write also runs off the session's per-capability queue (D-41): a subprocess on that goroutine would stall every later message from the same peer.

## D-55: A refused peer is told NOT_PAIRED, and the dialler translates the code back
Date: 2026-07-31
Status: accepted (refines D-52)
Context: D-52 made the listener close a connection whose handshake failed. The first implementation closed with `PROTOCOL_VIOLATION` for anything that was not already a `ProtocolError` — which is precisely the case an unpaired peer produces, since the authorize callback returns an ordinary error. The peer was therefore told it had malformed something, and its user sent looking in the wrong place. It also changed where the failure surfaces: the sending side used to reach its own trust-store check, and now the far end hangs up during Hello, before that check runs.
Decision: an authorize refusal closes with **`NOT_PAIRED` (§10, 0x04)**. And `conn.DialAddr` translates a remote application-error close into a `session.ProtocolError` carrying that code, so `session.ErrorCodeOf` works on a dial failure exactly as it does on a local one.
Rationale: the code is the only thing the far end can act on, so it has to name what actually happened. The translation is what lets the layer with a user in front of it say "that device has not paired with this one; run `openair pair` on both ends" instead of quoting `Application error 0x1 (remote)`. Both the CLI and the daemon do exactly that.
Consequences: the failure is now detected earlier — before any file is opened — which is better, but it means a caller's own pairing check is no longer the thing that produces the message. `TestSendRefusesUnpairedPeer` still asserts the advice reaches the user, and it now does so through the remote code rather than the local store.

## D-56: Android's daemon is a foreground service, and its prompts are notifications
Date: 2026-07-31
Status: accepted
Context: M4 gives the desktop `openaird`, so a file arrives whether or not a terminal is open. Android needed the same property and cannot have the same mechanism: D-31 puts the Go core in the app's own process via gomobile, so there is no second process to talk to and no IPC at all. Until now the v2 shell's `Receiver` was owned by the view model, which means the device stopped receiving the moment the activity was destroyed — which is most of the time anyone would want to send it something.
Decision: a **foreground service** (`ReceiverService`) holds the listener, the LAN announcement and the clipboard sink for the process, with the state in a process-wide `ReceiveSession` the UI observes. An inbound offer is published to whichever of the two can answer — the in-app dialog, or the notification's accept/decline actions — both completing the same deferred, so whichever the user reaches first decides and the other becomes a no-op. Unanswered within 60 s is a decline, matching D-53.
Rationale: it is the only mechanism Android offers for work that must outlive an activity, and `dataSync` is the type Android 14 requires such work to declare. Putting the prompt on the notification is what makes an unattended accept possible at all; without it the "daemon" would be running and unable to ask anyone anything.
Alternatives considered: *WorkManager* (rejected — it schedules work, it does not hold a socket open). *A separate process with IPC, mirroring the desktop* (rejected — D-31 chose in-process for good reasons and this would re-introduce the boundary it removed, for one platform).
Consequences, and one is a real limitation: **from Android 10 the system silently ignores a clipboard write from a process that is not in the foreground.** A received clipboard push therefore cannot reliably be pasted while the app is in the background — no error is reported, it simply does not happen. The write is attempted regardless for the foreground case, and the content is also put on a notification, which is what makes it reachable either way. `POST_NOTIFICATIONS` being refused does not stop the service; it costs the offer prompt while the app is backgrounded, and the transfer is then declined by timeout rather than accepted blindly.

## D-57: An AuthProof precedes the operation it authorises, and is spent by it
Date: 2026-07-31
Status: accepted (implements D-30 and PROTOCOL.md §6)
Context: §6 defines `AuthProof` and the string it signs — `"openair-owned-v1" || target_device_id || capID || msgType || nonce || issued_at` — but never says how a proof is attached to the request it authorises. The schema settles more than it first appears: `AuthProof` is a standalone control message with its own msgType (6) and carries no capID or msgType field of its own. So a proof cannot be verified when it arrives, because the signed input names an operation that has not been seen yet.
Decision: The initiator sends the proof on the control stream immediately before the message it authorises, under one hold of the write lock so nothing interleaves. The verifier holds received proofs and, for each inbound capability message, tries to verify the pending proofs against *this* message's capID and msgType. The first that verifies is consumed and cannot be used again; the message is then dispatched with the fact recorded in its context (`session.OwnedFromContext`).
Rationale: this is the only reading the schema permits without changing it, and it keeps the binding cryptographic rather than declared — a proof for a file read does not verify as a screen mirror, because the signature covers the operation. Verification runs on the control loop before the message is queued, so a proof cannot be spent by two messages racing each other.
Alternatives considered: adding `cap_id` and `msg_type` fields to `AuthProof` (rejected — it is a wire change to an interoperable message for something the verifier already knows, and the fields would be unauthenticated duplicates of what the signature already binds). Wrapping every Owned-level message in an envelope carrying its proof (rejected — every capability schema would gain an auth field, and §3's envelope would effectively grow a second header). Verifying at dispatch inside the capability (rejected — the capability would have to be trusted to do it, and `caps.Capability` deliberately documents that authorisation happens before Serve).
Consequences: pending proofs are bounded at 8 and stale ones are diagnosed at match time rather than pruned, so a peer whose clock has drifted is told `AUTH_EXPIRED` rather than `UNAUTHORISED` — the two send a user to different places. One ordering caveat is stated in the code rather than hidden: a proof on the control stream and a capability *stream* opened straight after are not ordered against each other by QUIC, so an Owned-level stream opener could race its own proof. No Phase 1 capability declares Owned, so nothing exercises it; a capability that does will need its proof on the stream itself, and that is a §6 edit rather than an implementation detail.

## D-58: §6.5's one-hour grace is enforced on the initiator, not the verifier
Date: 2026-07-31
Status: accepted (implements PROTOCOL.md §6.5, D-25)
Context: §6.5 requires three things at expiry: reject new Owned operations, let running ones finish, and abort them regardless after a grace of at most one hour. The first two have obvious homes. The third does not: the verifier is the party that would abort, and nothing on the wire tells it when the initiator's token expires.
Decision: The initiator enforces the cap. `identity.BeginOwned` refuses to start an operation once the token has lapsed and returns an `Operation` whose `Check` stays nil until expiry plus one hour and fails with `ErrGraceExpired` after it. The verifier enforces the other half: every *new* Owned request needs a fresh valid proof, and no auth-related rule aborts work already running.
Rationale: the cap is a statement about a timer only one machine can see. A verifier guessing at it would either kill legitimate long transfers — a 20 GB file started at hour one of a six-hour token may honestly run until hour seven — or invent a limit the spec does not describe. What the verifier *can* enforce is exactly what it can observe: no proof, no new operation. Together the two rules are §6.5 without either end pretending to know the other's clock.
Alternatives considered: carrying `expires_at` in `AuthProof` so the verifier can compute the cap (rejected — a wire change, and a self-reported expiry is only as trustworthy as the peer, which is the party the cap exists to bound). Capping each operation at one hour from its authorising proof (rejected — it silently forbids long transfers that §6.5 explicitly protects). Leaving the cap unimplemented (rejected — it is the sentence that stops expiry being extendable indefinitely).
Consequences: a compromised initiator that ignores its own timer keeps running work it started before expiry, which the cap was meant to bound. That is the honest limit of enforcing it where the timer lives, and it is bounded by the fact that no *new* operation can start. The rule belongs in the threat model beside D-30's policy-versus-cryptography note.

## D-59: The unlock credential travels over the local IPC socket; the daemon never prompts
Date: 2026-07-31
Status: accepted
Context: `openaird` holds the sealed privilege key, so it is the process that must unseal it (D-19). It has no terminal, no screen and no way to ask anybody anything. Something else has to obtain the passphrase, or the keystore key, and get it to the daemon.
Decision: The shell collects the credential and sends it in `DaemonUnlockRequest` over the existing local socket. `openair unlock DEVICE` reads a passphrase without echo when standard input is a terminal and from a pipe with `--passphrase-stdin`; the Android shell sends a key-encryption key the Keystore released after a device-credential challenge. The daemon never initiates a credential prompt.
Rationale: the socket is already the boundary that protects everything this daemon can do. It is 0600 in a 0700 directory, with a `SO_PEERCRED` uid check on Linux and an owner-only ACL on the named pipe (D-51); a process that could read a passphrase crossing it could equally open it and drive the already-unlocked daemon. Adding a second, weaker path — a daemon-side prompt routed to whichever UI happened to be subscribed — would widen the surface rather than narrow it, because the credential would then flow to *every* client capable of answering prompts.
Alternatives considered: a daemon-side prompt delivered as a `DaemonPrompt` to subscribed clients (rejected — it turns "who may see the credential prompt" into a question about event subscriptions, and a malicious local client subscribing for prompts would be handed the challenge). A setuid helper that unseals and passes back the key (rejected — a second privileged binary to audit, for a boundary that is already local). Reading the passphrase from an environment variable or a file (rejected — both persist it somewhere it was never meant to be).
Consequences: the credential exists in the CLI process's memory and crosses a socket, so it is in two address spaces rather than one. The daemon zeroes the decrypted key on expiry and locks its pages (D-19); the request payload itself is not zeroed, and saying so is more useful than implying otherwise. On platforms where the peer-credential check is unavailable — everything that is neither Linux nor Windows — the socket's file permissions are the whole of the protection.

## D-60: The protection tier is detected from the key material, not configured
Date: 2026-07-31
Status: accepted (implements D-21)
Context: `identity.LoadOrCreate` takes a tier, and a tier that disagrees with what is on disk is `ErrTierMismatch` rather than a silent downgrade. Something has to supply the right value on every start, for the daemon, the CLI and the Android shell alike.
Decision: `identity.DetectTier` reads the sealed container's KDF byte — kdf 0 is a keystore-held key-encryption key (tier 1), kdf 1 is Argon2id (tier 2), and an absent file is tier 3. The daemon, `LoadIdentity` on mobile, and every CLI path use it. Creating a privilege key is a separate, deliberate act: `openair protect` on the desktop, "Set up" in the Android shell.
Rationale: the file already knows the answer, and a flag would let a typo take a device out of Owned with no diagnostic beyond a refusal six hours later. Making creation explicit also puts the two consequences in front of a user at the moment they matter — that devices paired before this moment did not receive the key and must be paired again, and that a forgotten passphrase is unrecoverable by design (D-7's pinning).
Alternatives considered: a `--protect` flag on `openaird` (rejected — the typo case above, and a daemon that creates key material from a flag is a daemon that can be made to create it by an init script). Creating a privilege key automatically on first start (rejected — at tier 2 it needs a passphrase nobody has been asked for, and generating an unprotected one would advertise a tier the device does not offer, which D-21 exists to prevent).
Consequences: a device upgraded from an earlier build stays at tier 3 until someone runs `protect`, which is the correct default and is stated in the daemon's startup log. Pairings made before either side had a privilege key carry no pinned privilege key, so promotion to Owned is refused with an explanation rather than accepted into an access that could never verify.

## D-61: M6 ships tier 1 on Android and tier 2 on the desktop; Linux TPM sealing is deferred
Date: 2026-07-31
Status: accepted, with a gap stated
Context: D-21 ranks the tiers and notes that the maintainer's Fedora machine has TPM 2.0, so Linux *could* be tier 1. D-19 additionally prefers a keystore that signs internally over one that hands a key to the process. M6 had to ship something.
Decision: Android gets tier 1. The Android Keystore holds an AES-GCM key created with `setUserAuthenticationRequired(true)` and a six-hour authentication validity window; that key wraps a 32-byte key-encryption key, and only the wrapped blob is stored. Unlocking decrypts it after the platform's own credential prompt, and the six hours are enforced by the platform rather than by a timer in the process — which is the refinement D-19 asked for. The desktop gets tier 2: a passphrase through Argon2id with the parameters stored in the container. Linux TPM sealing is not implemented.
Rationale: the Android path is where the gap between "policy" and "hardware-enforced" is largest and the platform support is cleanest. TPM sealing is not one piece of work but two — sealing to PCRs for the always-on case and sealing with user presence for the interactive one (D-21) — plus a new dependency and tests that cannot run on hardware without a TPM. Shipping tier 2 everywhere on the desktop is honest and works; claiming tier 1 there would not.
Alternatives considered: blocking M6 on TPM support (rejected — it delays the gate every later unattended feature needs, for a platform improvement that can land on its own). Treating the presence of `/dev/tpmrm0` as tier 1 without sealing to it (rejected — that is a tier claim with nothing behind it, and peers make Owned decisions partly on the claimed tier).
Consequences: the maintainer's primary Linux machine runs on the weaker of the two branches D-19 compared, which is exactly the situation D-21 flagged and does not resolve. The cold threat — a stolen disk, a synced backup — is bounded by the passphrase's strength rather than by hardware attempt limits, and `openair protect` refuses passphrases under twelve characters for that reason. Android's key is invalidated if the user changes their screen lock, which destroys the wrapped key-encryption key; the shell reports that owned access must be set up again rather than retrying, and pairings survive because they ride the identity key (D-20).

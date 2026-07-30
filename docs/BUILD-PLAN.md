# OpenAir 2.0 — Build Plan

Execution plan from nothing to complete. Every milestone ships something you can
actually run; none requires a later one to be useful.

**Who this is for.** Each milestone is scoped so an agent can pick it up, read a
named and bounded set of documents, and finish it. Complexity tiers say which
model to assign. The wave map says what can run simultaneously.

---

## 0. Running this

### If you were told "do wave N" — you are the orchestrator

1. Confirm the previous wave is done. Waves are barriers; a half-finished
   dependency produces agents that build against interfaces that then change.
2. Spawn **one agent per task** in the wave, concurrently, assigning the model
   by tier (§2). Give each agent its task ID, its "Read" list verbatim, and §0's
   worker rules below. Do not paste this whole document into an agent prompt.
3. As each reports, verify before accepting: `go build ./... && go vet ./... &&
   go test ./...`, plus `GOOS=windows go build ./...`. Read the diff against the
   spec section the agent was given. An agent reporting success has not been
   verified by anyone yet.
4. **You** update `docs/functionality.md` and write any `docs/decision-tree.md`
   entries, then commit. Workers do neither — see below.

Do not delegate **X1 (BBR)**. A congestion controller that is subtly wrong shows
up as a performance regression nobody can localise months later.

### If you were told "do task M1c" (or similar) — you are a worker

- Read `AGENTS.md`, then **only** the sections in your task's "Read" list. Do not
  read `decision-tree.md` or `PROTOCOL.md` end to end; both are reference works
  and reading either in full spends your context before you write a line.
- Implement against the interfaces already defined in `internal/*/types.go`
  (see below). If one is wrong, say so in your report — do not change it
  silently, because other agents are compiling against it right now.
- Write tests. A milestone without tests is not done (§1).
- **Do not commit.** Multiple agents share one checkout — worktrees and branches
  are forbidden by AGENTS.md §4 — so concurrent commits race the git index.
- **Do not edit `docs/decision-tree.md` or `docs/functionality.md.`** Concurrent
  appends conflict and entry numbering collides. Report instead.
- Report back: what you built, which files, any decision you made that deserves
  a log entry, and **anything in the spec that turned out to be wrong**. That
  last one is expected rather than exceptional — compiling the schemas found six
  defects in `PROTOCOL.md` (D-34), and more will surface during implementation.

### Shared interfaces are already defined

`internal/identity/types.go`, `internal/session/types.go`,
`internal/caps/types.go` and `internal/conn/types.go` hold the contracts every
Phase 1 task compiles against — `DeviceID`, `Identity`, `TrustStore`,
`Envelope`, `Session`, `Stream`, `Capability`, `Dialer`, `Listener`.

They exist so parallel agents cannot invent incompatible boundaries. Function
bodies are `panic("M1a: unimplemented")` where a task will fill them in; the
signatures are the agreement. Changing one is a cross-task decision and belongs
with the orchestrator.

---

## 1. Rules this plan follows

**Vertical slices, not layers.** The architecture is layered — identity, session,
capabilities — but the *plan* is not. Each milestone cuts through every layer it
needs and ends at something runnable. You never build three things before the
first is usable.

**Definition of done, applied to every milestone without exception:**
1. The feature runs end to end, by hand, on two real endpoints.
2. Unit tests for the logic; an integration test for the slice.
3. `go build ./... && go vet ./... && go test ./...` clean, and `GOOS=windows go build ./...` clean (D-32 — Windows must not silently rot).
4. `docs/functionality.md` updated in the same commit (AGENTS.md §2).
5. Any decision taken along the way logged in `docs/decision-tree.md`, including the Mermaid trees (AGENTS.md §1).

**Reference implementations already exist.** `oabench/bench/` is working, measured
code: `tlsutil.go` is the D-7 identity and pinning mechanism, `framing.go` is the
v1.0 chunk header, `quic.go` and `tcp.go` are the transfer engines, `latency.go`
is the probe. **Port these; do not reinvent them.** They have been run against a
shaped network and their numbers are in D-13, D-17 and D-33.

---

## 2. Complexity tiers

| Tier | Meaning | Assign to |
|---|---|---|
| **T1 · Mechanical** | Fully specified, little judgment. Porting, wiring, codegen, adapters against a fixed interface. | Sonnet 5 |
| **T2 · Standard** | Normal implementation. Design choices exist but sit inside a settled contract. | Opus 5 |
| **T3 · Deep** | Subtle correctness: concurrency, crypto, protocol state machines, or a design still open. Failure is silent and expensive. | Opus 5 |

A T3 task assigned to a cheap model is how you get a protocol that looks right
and is wrong. A T1 task on an expensive model is only wasted money — pick the
asymmetry you prefer.

---

## 3. Milestone map

| # | Milestone | Tier | Wave | Depends on | Usable on its own as |
|---|---|---|---|---|---|
| **M1** | Direct transfer | T2 | 1 | — | `openair send file 10.0.0.5:9000` — v1.0 minus discovery |
| **M2** | Pairing & trust store | T3 | 2 | M1 | Transfers only to devices you paired |
| **M3** | LAN discovery | T2 | 2 | M1 | No typing addresses — LocalSend-equivalent |
| **M4** | Daemon & CLI | T2 | 3 | M1 | Background service; receive without a terminal open |
| **M5** | Clipboard | T1 | 3 | M4 | Push clipboard between machines |
| **M6** | Unlock & Owned access | T3 | 4 | M2, M4 | Unattended access, gated |
| **M7** | Rendezvous | T2 | 5 | M2 | Find your devices across networks |
| **M8** | Relay | T2 | 5 | M7 | Connect from anywhere, always |
| **M9** | Punching & migration | T3 | 6 | M8 | Direct P2P; survives WiFi→mobile |
| **M10** | remotefs browse | T2 | 6 | M6 | Browse another device's files |
| **M11** | remotefs streaming | T3 | 7 | M10 | Play a remote video, seek instantly |
| **M12** | Notifications | T2 | 6 | M4 | Phone notifications on desktop |
| **M13** | Auto-clipboard | T2 | 6 | M5 | Clipboard syncs without asking |
| **M14** | Input | T3 | 7 | M6 | Control another machine |
| **M15** | Mirror | T3 | 8 | M14 | See another machine's screen |

### Cross-cutting tracks — start any time, no dependencies

| # | Track | Tier | Notes |
|---|---|---|---|
| **X1** | BBR into the quic-go fork | T3 | D-14, D-16. Blocks nothing; needed before the M1 benchmark gate is meaningful on lossy paths |
| **X2** | Windows USO + URO | T3 | D-22, D-23. Deferred to Phase 2 by D-32, but the fork work is independent |
| **X3** | Threat model | T2 | PRD R29. Mostly assembly from existing decisions |
| **X4** | Envelope golden vectors | T1 | HLD §5. Small, and it protects everything downstream |
| **X5** | Android shell | T2 | Compose UI over the gomobile core (D-31). Tracks M1–M5 one milestone behind |
| **X6** | CI | T1 | build/vet/test, `GOOS=windows` build, `buf lint`, `buf breaking`, nightly netem matrix |

---

## 4. Parallelisation waves

Run everything in a wave simultaneously. Waves are barriers.

```
Wave 1   M1                          + X1 X3 X4 X6        (6 agents)
Wave 2   M2  M3                      + X2 X5              (4 agents)
Wave 3   M4  →  M5                   + X5                 (3 agents)
Wave 4   M6                          + X5                 (2 agents)
Wave 5   M7  →  M8                                        (2 agents)
Wave 6   M9  M10  M12  M13                                (4 agents)
Wave 7   M11  M14                                         (2 agents)
Wave 8   M15                                              (1 agent)
```

**M1 itself splits four ways** — see §5. That is the widest point in the plan and
the one worth staffing hardest, because everything downstream compiles against
the interfaces it defines.

Two rules for concurrent agents, both from AGENTS.md §3: claim one package, and
never rewrite another agent's decision entry — append a correction instead. If
two agents need the same file, sequence them rather than racing.

---

## 5. Milestones in detail

Each lists exactly what to read. **Read only what is listed.** The decision tree
is 34 entries and `PROTOCOL.md` is 900 lines; loading either whole is how an
agent burns its context before writing a line of code.

---

### M1 · Direct transfer — T2, wave 1

**Goal.** `openair recv` on one machine, `openair send ./file 10.0.0.5:9000` on
another. Fingerprint shown and accepted interactively; no pairing, no discovery.

**Usable as:** v1.0's core, minus discovery, over QUIC.

Splits into four tasks that can run at once, then one integration:

| Task | Tier | Package | Read |
|---|---|---|---|
| **M1a** Envelope & framing | T2 | `internal/session` | PROTOCOL.md §0, §3, §10 · `internal/wire/` · D-34 · implement `EncodeEnvelope`/`DecodeEnvelope` in `session/types.go` |
| **M1b** Identity & TLS | T2 | `internal/identity` | PROTOCOL.md §1, §2 · **port `oabench/bench/tlsutil.go`** · D-7 · implement `Identity` + `TrustStore` from `identity/types.go` |
| **M1c** Chunk engine | T2 | `internal/caps/files` | PROTOCOL.md §8 · **port `oabench/bench/{framing,transfer,quic}.go`** · D-13, D-33 · implement `caps.Capability` |
| **M1d** Dial & accept | T1 | `internal/conn` | PROTOCOL.md §1 · `oabench/bench/quic.go` for Config values · implement `Dialer.DialAddr` + `Listener` |
| **M1e** Integration & CLI | T2 | `cmd/openair` | all of the above, after they land |

**Tests.** Golden vectors for the envelope (shared with X4). Chunk plan covers
every byte exactly once — `oabench/bench/bench_test.go` already has this, port
it. Round-trip a 1 GiB file and compare digests. Integration test over loopback.

**Watch for.** Default to **one or two data streams, never eight** (D-13, D-33) —
v1.0's worker count is actively harmful on QUIC. Wire values are offset by one
from generated enum values; convert, never cast (D-34).

**Done when** a file moves between two machines and its SHA-256 matches.

---

### M2 · Pairing & trust store — T3, wave 2

**Goal.** QR or six-digit code, both users confirm, keys pinned. Unpaired peers
rejected.

**Usable as:** transfers restricted to devices you actually paired.

**Read.** PROTOCOL.md §2, §5, §6.1, §6.4 · D-7, D-20, D-21, **D-30 (trust store
schema table)** · PRD R1–R5.

**Tests.** SAS is identical on both sides and role-independent. A key mismatch
hard-fails and is *not* retryable. Trust store survives restart. Revocation takes
effect mid-session.

**Why T3.** The short authentication string is the entire security of pairing. A
transcript that is not role-independent, or a comparison that is not
constant-time, produces something that looks like it works and is not secure.
Never offer a skip-verification path.

---

### M3 · LAN discovery — T2, wave 2

**Goal.** Peers appear automatically; no addresses typed.

**Usable as:** LocalSend-equivalent LAN transfer.

**Read.** PROTOCOL.md §15 · `openair-gui/internal/sender/discover.go` (v1.0's
working mDNS) · HLD §3.2.

**Tests.** Two instances find each other under 3 seconds (PRD R6). Unicast
fallback works with multicast blocked. A hostile announce changes nothing,
because peers are still authenticated by pinned key.

**Watch for.** `_openair._udp`, not v1.0's `_tcp` — v2 is QUIC. Discovery emits
candidates and never dials.

---

### M4 · Daemon & CLI — T2, wave 3

**Goal.** `openaird` runs in the background; `openair` drives it over local IPC.

**Usable as:** receive files without keeping a terminal open.

**Read.** **D-29 (IPC reuses the session envelope, not gRPC)** · HLD §2 ·
PROTOCOL.md §3.

**Tests.** Daemon survives client disconnect. Socket permissions reject other
users. Idle RSS under 50 MB (PRD R30).

**Watch for.** The IPC socket is a **local trust boundary** — anything that can
open it drives the daemon. Filesystem permissions and a Windows named-pipe ACL
are security requirements, not hygiene.

---

### M5 · Clipboard — T1, wave 3

**Goal.** `openair clip push <device>`.

**Usable as:** clipboard between machines.

**Read.** PROTOCOL.md §9 · D-20 (runs on the identity key) · PRD R18–R20.

**Tests.** Round-trip UTF-8 including emoji. Oversized content rejected, not
buffered.

**Watch for.** Identity key, not privilege key. Gating this would stop clipboard
working whenever an unlock expired — the policy would become visible exactly
where users tolerate it least.

---

### M6 · Unlock & Owned access — T3, wave 4

**Goal.** Biometric or passphrase unlock, six-hour per-peer token, `AuthProof` on
Owned requests.

**Usable as:** the gate every later unattended feature needs.

**Read.** PROTOCOL.md §6 (all of it) · **D-18, D-19, D-20, D-21, D-25, D-30** ·
PRD R3, R4, K10 · Appendix A of PROTOCOL.md for the at-rest format.

**Tests.** Expired token rejects new operations but lets in-flight finish, capped
at one hour. Replayed `AuthProof` rejected. A proof for one peer fails against
another. Tier 3 device cannot reach Owned.

**Why T3.** This is the security core. Specific hazards: the decrypted key must
be in locked pages, never in a core dump, zeroed on expiry; per-peer scope is
currently **policy, not cryptography** (D-30 records the refinement that would
fix it, deliberately not adopted); and `SessionKill` is a courtesy message —
enforcement is local refusal, or R4's guarantee depends on the goodwill of the
party being revoked.

---

### M7 · Rendezvous — T2, wave 5 · M8 · Relay — T2, wave 5

**Goal.** Find peers across networks (M7); reach them regardless (M8).

**Usable as:** M7 alone shows a device's endpoints. M8 makes cross-network
transfer actually work, over the relay, before any punching exists.

**Read.** PROTOCOL.md §16 (M7), §17 (M8) · HLD §2 · PRD R7, R8, NG5.

**Tests.** Registration signature verified; a forged one rejected. Expiry beyond
10 minutes refused. Relay refuses delivery to an unauthenticated DeviceID.
End-to-end encryption holds through the relay.

**Watch for.** Both are self-hostable and neither ever sees plaintext. What each
operator *does* learn is written in PROTOCOL.md and belongs in X3's threat model.

---

### M9 · Punching & migration — T3, wave 6

**Goal.** Race direct paths, upgrade off the relay live, survive network changes.

**Usable as:** transfers that keep running across WiFi→mobile (PRD R9).

**Read.** PROTOCOL.md §18 · HLD §3.3 · PRD R9, M5, K2 · `oabench/netem/lab.sh`
for the test harness.

**Tests.** Netem namespaces simulating symmetric NAT. Migration mid-transfer
without restart. Path death falls back to relay. Hysteresis prevents flapping.

**Why T3.** Distributed timing with no clean failure mode. `start_at` must
synchronise both sides within ~50 ms without trusting clocks.

---

### M10 · remotefs browse — T2 · M11 · remotefs streaming — T3, waves 6–7

**Read.** PROTOCOL.md §11 · PRD R14–R17, K8.

**Tests.** Traversal attempts rejected. Paginated listing of 100k entries. Short
reads handled. M11: seek in a 40 GB file under 1 s LAN, 3 s relayed.

**Watch for.** Dumb server, smart client — the source serves byte ranges and does
not know it is serving video. Read-ahead and caching are the client's business,
and a cache holding someone else's files must be encrypted and size-capped.

---

### M12 · Notifications — T2 · M13 · Auto-clipboard — T2, wave 6

**Read.** PROTOCOL.md §12 (M12), §9 (M13) · PRD R21–R23, K7 (M12), R19, K3 (M13).

**Tests.** Filtering happens on the source — assert excluded content never
reaches the wire. Dismiss propagates. M13: loop suppression under simultaneous
edits.

---

### M14 · Input — T3 · M15 · Mirror — T3, waves 7–8

**Read.** PROTOCOL.md §13 (M14), §14 (M15) · **D-9 and D-24 before writing any
mirror code** · PRD R24–R26, K6.

**Tests.** Stale pointer events dropped, key events never dropped. Stuck-key
safety release. M15: glass-to-glass latency under load, with `oabench`'s probe
(D-17) as the measurement method.

**Why T3, and read this first.** **ADR-4 is still open.** §14 is provisional:
stream-per-frame with `RESET_STREAM` is the current leaning after D-24, not a
decision. M15 begins with a spike comparing that against datagrams, and the spike
result gets logged before implementation starts.

---

## 6. Context budget

For any task, read in this order and stop when you can start:

1. **`AGENTS.md`** — always, it is short and it governs how you work.
2. **The milestone's "Read" list** — precisely those sections.
3. **`docs/functionality.md`** — the module you are touching.
4. Anything a decision entry explicitly cross-references.

Do **not** read `decision-tree.md` end to end, or `PROTOCOL.md` end to end. Both
are reference works. The status table at the top of the decision tree is the
index; use it to find the three entries that matter to you.

If a decision seems wrong, append a superseding entry — never edit the old one
(AGENTS.md §1).

---

## 7. What is still open

Two things this plan cannot schedule around, because they are not decided:

- **ADR-4 / D-9, the media plane.** M15 is blocked behind a spike, not merely
  behind M14.
- **D-30's delegation refinement.** Optional. If adopted it changes M6, so decide
  before M6 starts rather than during it.

And two measurements that inform but do not block: on-device Android throughput
and battery (`oabench/androidkit/`), and the Windows CPU figures now that the
harness reports them (`oabench/winkit/`).

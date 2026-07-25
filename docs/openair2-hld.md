# OpenAir 2.0 — High-Level Design

**Author:** Shreyash · **Status:** Draft v1 · **Companion docs:** PRD v2, build plan

---

## 1. Design approach

The system is built as a **capability platform on top of a connectivity substrate**. Everything below the dashed line exists so that everything above it can pretend the network doesn't exist:

```
┌───────────────────────────────────────────────────────────┐
│  CAPABILITIES (plugins over the session)                  │
│  files · remotefs · clipboard · notifications ·           │
│  input · mirror                                           │
├───────────────────────────────────────────────────────────┤
│  SESSION LAYER                                            │
│  capability negotiation · message routing ·               │
│  stream/datagram allocation · flow priority               │
├ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─┤
│  CONNECTION MANAGER ("pathfinder")                        │
│  path candidates: LAN / hole-punched / relayed            │
│  path racing · live migration · keepalive                 │
├───────────────────────────────────────────────────────────┤
│  TRANSPORT: one QUIC connection per peer (quic-go)        │
│  reliable streams + RFC 9221 datagrams · TLS 1.3          │
├───────────────────────────────────────────────────────────┤
│  IDENTITY:  device keypair · TOFU store · trust levels    │
├───────────────────────────────────────────────────────────┤
│  DISCOVERY: mDNS (LAN) · rendezvous client (WAN)          │
└───────────────────────────────────────────────────────────┘
```

Guiding principles:
1. **One connection per peer.** All capabilities multiplex over a single QUIC connection. No capability opens its own sockets. This is what makes "works over NAT, always" a structural property instead of a per-feature chore.
2. **Path-agnostic capabilities.** A capability sees `Session` (open stream, send datagram, receive). It cannot tell whether the path is LAN, punched or relayed — it can only query a QoS hint (bandwidth class, RTT) to adapt quality.
3. **Core once, UIs thin.** One Go core compiled everywhere; per-platform shells are thin adapters for OS APIs (clipboard, notifications, capture, input injection) and UI.
4. **Spec-first.** Every wire message lands in `docs/PROTOCOL.md` before it lands in code. Versioned envelope from message one.

## 2. System context & deployment

```
 Home LAN                                Internet             Away
┌─────────────────────┐          ┌────────────────────┐   ┌─────────────┐
│ Linux desktop        │          │ Rendezvous (VPS)   │   │ Windows     │
│  openaird ───────────┼──────────┤  pubkey→endpoints  ├───┤ laptop      │
│                      │  direct/ │                    │   │  openaird   │
│ Android phone        │  punched │ Relay (VPS, :443)  │   │             │
│  OpenAir app (fg svc)│  paths   │  ciphertext pipe   │   │             │
└─────────────────────┘          └────────────────────┘   └─────────────┘
```

- **openaird**: always-on daemon per device. Desktop: system service + tray UI talking to it over local IPC (gRPC over unix socket / named pipe). Android: the same Go core via gomobile, hosted in a persistent foreground service; Kotlin/Compose UI on top.
- **Rendezvous**: tiny stateless-ish service mapping device pubkey → {current endpoints, relay home, presence}. Self-hostable, one binary.
- **Relay**: DERP-style ciphertext forwarder listening on TLS/443. Never holds keys, never sees plaintext. Self-hostable.
- Rendezvous and relay can run in one process for small deployments.

## 3. Component design

### 3.1 Identity & trust (`internal/identity`)
- Ed25519 long-term keypair per device; DeviceID = SHA-256(pubkey) truncated, base32.
- TLS: per-device self-signed cert; peer verification = cert pubkey must match pinned key. No CA anywhere.
- Pairing protocol: QR (or 6-digit PIN) carries {DeviceID, pubkey fingerprint, LAN hint}. Both sides confirm fingerprint → keys pinned in the trust store.
- Trust store record: `{deviceID, pubkey, name, level: owned|trusted, capabilities granted, created, lastSeen}`. Revocation deletes the record and broadcasts a session-kill.
- **Owned-level gate (from PRD K10):** using Owned access from a device can optionally require a local unlock (OS biometric/PIN) — the key alone shouldn't be enough. Decision deferred to ADR-3.

### 3.2 Discovery (`internal/discovery`)
- **LAN:** mDNS advertise/browse `_openair._udp`, TXT = {deviceID, port, protoVersion}. Unicast UDP announce fallback on 224.0.0.167-style multicast-hostile networks (LocalSend pattern). Android: NsdManager wrapper + multicast lock.
- **WAN:** rendezvous client maintains a registration (signed, so only the key holder can update endpoints) with heartbeat. Lookup by DeviceID returns candidate endpoints + relay home.
- Output of this layer: a stream of `PeerCandidate{deviceID, addrs[], via}` events into the connection manager. Discovery never dials.

### 3.3 Connection manager (`internal/conn`) — the heart
State machine per paired peer: `idle → resolving → connecting → connected(path) → migrating → connected(path') → idle`.

Establishment ("connect to peer X"):
1. Gather candidates: LAN addrs (mDNS), reflexive addrs (STUN), relay path (always available).
2. **Start on the relay immediately** — session usable in ~1 RTT to relay.
3. In parallel, race direct candidates: LAN dial + coordinated UDP hole punch (both sides spray, synchronized via rendezvous/relay signaling).
4. On a better path succeeding, **migrate the QUIC connection** (connection IDs make this native) without breaking streams.
5. Continuous: keepalives per path, re-race on network change events (OS network monitor), downgrade to relay on path death.

Exposes: `Dial(deviceID) → Session`, `OnInbound(func(Session))`, `PathInfo()` (for QoS hints).

### 3.4 Session layer (`internal/session`)
- Capability negotiation on connect: both sides exchange `Hello{protoVersion, capabilities[], deviceInfo}`.
- Stream allocation: control stream (stream 0) + per-operation streams opened by capabilities. Datagram channel shared, with a tiny capability-ID prefix byte for demux.
- **Priority classes:** `interactive` (input, clipboard) > `media` (mirror datagrams) > `bulk` (file/remotefs streams). Enforced via quic-go stream priorities + sender-side pacing of bulk writers; input additionally rides datagrams so it can never queue behind a stream.
- Authorization middleware: every inbound capability request checked against trust level + granted capabilities before reaching the plugin.

### 3.5 Capability framework (`internal/caps`)
```go
type Capability interface {
    ID() string            // "files", "remotefs", ...
    Serve(sess Session)    // handle inbound
    // client side is capability-specific typed APIs
}
```
Each capability defines its messages in the shared schema (protobuf), its own doc section in PROTOCOL.md, and degrades against `PathInfo` hints.

### 3.6 Capabilities (summary designs)

**files** — offer/accept, then N parallel QUIC streams carrying chunk frames `{offset, size, data}` (direct descendant of v1.0's 12-byte header format), SHA-256 per chunk, resume via manifest of verified chunks. Bulk priority.

**remotefs** — request/response over streams: `Stat`, `List`, `Read{path, offset, len}`, `Thumb`. Range reads are the primitive; a media player on the client just issues reads as it plays/seeks. Client keeps a small read-ahead window (adaptive: RTT × bitrate) and an LRU disk cache (encrypted at rest, size-capped). This is deliberately "dumb server, smart client" — the source device streams bytes, it doesn't know it's serving a video.

**clipboard** — `Push{mime, payload}` message; auto-sync mode watches OS clipboard (desktop) and pushes on change with a loop-suppression tag. Android: manual/share-sheet path guaranteed, auto-read best-effort per OS policy.

**notifications** — source device (mainly Android via NotificationListenerService) emits `Posted{key, app, title, body, icon}` / `Removed{key}`; sinks render natively (libnotify, Windows toast). `Dismiss{key}` flows back. Per-app allowlist evaluated on the *source* so filtered content never leaves the device.

**input** — datagrams: `{seq, type: key|ptr|touch, payload}`; receiver applies latest-wins for pointer, ordered-within-window for keys. Injection adapters: SendInput (Win), XTest/libei (Linux), Accessibility/InputManager (Android target). Capture adapters on the controller side.

**mirror** — capture (Desktop Duplication / PipeWire / MediaProjection) → hardware encoder (H.264 first) → packetizer → QUIC datagrams with app-level FEC + `Nack/Idr` control messages on a stream. Adaptive bitrate from PathInfo + loss feedback. Fallback path (ADR-4): raw RTP-over-UDP à la Moonlight, keyed from the session, if datagrams can't hold latency.

### 3.7 Per-platform shells
- **Linux/Windows:** daemon + tray app; OS adapter interfaces (`Clipboard`, `Notifier`, `Capturer`, `Injector`) with per-OS implementations behind build tags.
- **Android:** gomobile-bound core in a foreground service; Kotlin implements the same adapter interfaces over Android APIs; Compose UI. SAF for filesystem exposure.
- macOS: compiles from the desktop shell, adapters best-effort.

## 4. Key flows (sequences)

**Pairing (once):** A shows QR → B scans → B dials A over LAN → TLS with both certs unpinned → both display fingerprint short-code → user confirms on both → keys pinned, level=trusted.

**AFK access (S3):** Laptop asks rendezvous for desktop → gets endpoints+relay → relay session up (<1 s) → hole punch races in background → laptop browses remotefs, starts `Read`-streaming a video → punch succeeds → QUIC migrates to direct path mid-playback → bitrate hint rises → later user opens mirror+input session on same connection.

**Notification:** phone NLS fires → allowlist check → `Posted` over control-ish stream → desktop toast → user hits dismiss → `Dismiss` back → phone cancels notification.

## 5. Cross-cutting

- **Wire format:** protobuf messages in a length-prefixed envelope `{version, capID, msgType, payload}`. PROTOCOL.md is normative; goldens tested in CI.
- **Security:** E2E = TLS 1.3 with pinned self-signed certs (ADR-2 weighs Noise_IK); relay/rendezvous see ciphertext + metadata only; threat model doc covers stolen-device, malicious-relay, malicious-LAN-peer.
- **Observability:** structured logs, per-path metrics (RTT, loss, goodput), qlog on demand; benchmark harness is a first-class tool in `cmd/oabench`.
- **Testing:** netem-based lab (loss/latency/NAT simulation via network namespaces) so hole punching and migration are CI-testable without real CGNAT.

## 6. Architecture decision records (to write, with current leaning)
- **ADR-1 Transport = QUIC** (accepted): rationale in research doc; escape hatch = parallel-stream bulk mode if benchmark gate fails off-Linux.
- **ADR-2 Session crypto:** TLS-1.3-pinned (leaning) vs Noise_IK. Decide before transport code.
- **ADR-3 Owned-access second factor:** local unlock required? (leaning yes, configurable).
- **ADR-4 Media plane:** QUIC datagrams (leaning, try first) vs raw RTP/UDP sidecar.
- **ADR-5 Android core:** gomobile-bound Go core (leaning) vs Kotlin reimplementation of the protocol. gomobile keeps one protocol implementation; cost is binding friction and APK size.
- **ADR-6 Consensus/replication from the old 2.0 protocol draft (STPB, manifest replication):** explicitly **dropped** for this vision — pairwise sessions need no consensus; revisit only if multi-device coordination features (e.g. one clipboard across N>2 devices with conflict rules) demand it.

## 7. Build order (mirrors PRD phases)
1. identity + trust store → session over plain LAN QUIC → files + manual clipboard → oabench gate.
2. rendezvous + STUN + punch + relay + migration → netem CI.
3. remotefs + notifications + auto-clipboard.
4. input + mirror, desktop targets first, Android target last.

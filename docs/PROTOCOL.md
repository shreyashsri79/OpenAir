# OpenAir Protocol v2 — Wire Specification

**Status:** Draft 0.1 · **Protocol version:** 1 · **Companion docs:** PRD v2, HLD, `decision-tree.md`

Normative. Where this document and the implementation disagree, this document is
correct and the implementation is a bug. Decisions are traceable to entries in
`docs/decision-tree.md`, cited as (D-*n*).

**Scope.** Every capability and every phase discussed to date: the envelope,
session establishment, pairing, authorisation and flow control (Phase 1);
`files` and `clipboard` (Phase 1); discovery, rendezvous, relay and connection
establishment (Phase 2); `remotefs` and `notifications` (Phase 3); `input` and
`mirror` (Phase 4). Each section carries its phase, so the document can be split
along those lines later.

**Maturity varies and is marked.** Phase 1 sections are specified against
decisions that are settled and, in the transport's case, measured. Later
sections are specified against decisions that are settled but unbuilt, so they
should be expected to move on contact with implementation. §14 (`mirror`) is
explicitly **provisional** — ADR-4 is still open (D-9).

The key words MUST, MUST NOT, SHOULD, SHOULD NOT and MAY are to be interpreted
as described in RFC 2119.

---

## 0. Conventions

- All integers are **little-endian**, matching OpenAir v1.0's chunk header, which
  v2 inherits byte-for-byte (D-4).
- Sizes are in bytes unless stated otherwise.
- Structured messages are **protobuf**; bulk data frames are **raw binary**. A
  1 MiB file chunk is never wrapped in protobuf — the hot path stays free of
  parsing (§8.3).
- `SHA-256` means the full 32-byte digest unless a truncation is given.
- Base32 means RFC 4648 lowercase, no padding.

---

## 1. Transport binding

OpenAir runs over **one QUIC connection per peer** (D-6), using quic-go with a
vendored congestion controller (D-14, D-16) and a Windows fast path (D-22, D-23).

- ALPN: `openair/1`. A peer offering no matching ALPN MUST be rejected.
- TLS 1.3 only. Earlier versions MUST be refused.
- QUIC datagrams MUST be enabled at the transport, so that capabilities added
  later can use them without a version bump. Phase 1 does not send any.

### 1.1 Streams

| Stream | Direction | Purpose |
|---|---|---|
| First bidirectional stream opened by the initiator | bidi | **Control stream.** Carries all enveloped session-layer and capability control messages. |
| Subsequent bidirectional streams | bidi | Per-operation streams opened by capabilities. |

The control stream MUST remain open for the life of the session. Closing it
terminates the session.

---

## 2. Identity and keys

Every device holds **two Ed25519 keypairs** (D-20):

| Key | Gated? | Role |
|---|---|---|
| **Identity key** | never | Terminates TLS. Keeps the device reachable and authorises capabilities granted at pairing (Trusted level). |
| **Privilege key** | yes | Encrypted at rest (D-19). Required to authorise Owned-level unattended operations (§6). |

```
DeviceID = base32( SHA-256( identity_public_key )[0:10] )     // 16 characters
```

TLS uses a self-signed certificate whose key **is** the identity key. Peer
verification compares the presented certificate's raw public key against the
pinned value — no certificate authority, no chain, no hostname check (D-7). A
mismatch MUST fail the connection and MUST surface as a re-pair prompt, never as
a retryable error.

---

## 3. Envelope

Every message on the control stream, and the first message on every
capability stream, is framed as:

```
 0        1        2                 4                                 8
 +--------+--------+-----------------+---------------------------------+
 | ver    | capID  | msgType (u16)   | length (u32)                    |
 +--------+--------+-----------------+---------------------------------+
 | payload — `length` bytes, protobuf                                  |
 +---------------------------------------------------------------------+
```

- `ver` (u8) — envelope version. This document defines **1**. A receiver seeing
  an unknown `ver` MUST close the connection with `PROTOCOL_VIOLATION`; the
  envelope is not forward-compatible by design, because everything else depends
  on parsing it correctly.
- `capID` (u8) — capability, per Appendix B. `0` is the session layer itself.
- `msgType` (u16) — message type within that capability. Values are enumerated
  per capability in the schemas (`ControlMessageType`, `FilesMessageType`, and so
  on in `proto/openair/v1/`). An earlier revision of this document defined the
  field but never gave it values, which made the envelope unimplementable.
- `length` (u32) — payload length. Receivers MUST reject `length` greater than
  **16 MiB** with `MESSAGE_TOO_LARGE`. Bulk data is not sent this way (§8.3), so
  no legitimate control message approaches this.

Header is 8 bytes, fixed, unparsed by protobuf, so demultiplexing costs no
allocation.

**Wire values are not enum values.** proto3 reserves 0 for `UNSPECIFIED`, so
every enum in the schemas is offset by one from the numbering used in this
document. The capID byte for `files` is `0x01`; `CAPABILITY_ID_FILES` is `2`.
Encoders must convert rather than cast. The same applies to `TrustLevel`,
`ProtectionTier`, `ConsentScope` and `PathClass`.

### 3.1 Unknown messages

A receiver that does not recognise `capID` MUST ignore the message and continue.
A receiver that recognises `capID` but not `msgType` MUST ignore the message and
continue. This is what makes mixed-version fleets viable (PRD R32) — new message
types are additive and never fatal.

---

## 4. Session establishment

Immediately after the QUIC handshake, the initiator opens the control stream and
both peers send `Hello`. Neither side may send any other message until it has
both sent and received one.

```protobuf
message Hello {
  uint32 proto_version   = 1;  // highest supported; this document is 1
  string device_id       = 2;  // §2, MUST match the TLS identity key
  string display_name    = 3;
  string platform        = 4;  // "linux" | "windows" | "android" | "darwin"
  repeated Capability capabilities = 5;
  uint32 protection_tier = 6;  // §7.3, per D-21
}

message Capability {
  uint32 id      = 1;  // Appendix B
  uint32 version = 2;  // capability-local, independent of proto_version
}
```

**Negotiation.** The effective protocol version is the lower of the two
`proto_version` values. A capability is usable only if both peers list its `id`;
its effective version is likewise the lower of the two. Capabilities listed by
only one peer MUST be silently dropped, not treated as an error.

`device_id` MUST equal the DeviceID derived from the TLS certificate's public
key. A mismatch is a `PROTOCOL_VIOLATION` — it means the peer is claiming an
identity it cannot prove.

---

## 5. Pairing

Pairing is trust-on-first-use and happens once (PRD R2). It runs on a connection
where **neither side has a pinned key yet**, so TLS provides confidentiality but
no authentication — the short authentication string in §5.2 is what actually
establishes trust.

### 5.1 Out-of-band offer

Device A displays a QR code, or a short code for manual entry, containing:

```protobuf
message PairOffer {
  string device_id            = 1;
  bytes  identity_fingerprint = 2;  // SHA-256(identity_public_key)[0:16]
  repeated string lan_hints   = 3;  // "host:port" candidates
  uint32 proto_version        = 4;
}
```

B scans it and dials A. B MUST verify that A's presented TLS key matches
`identity_fingerprint` before proceeding; a mismatch aborts pairing. The offer
authenticates A to B only — §5.2 authenticates B to A.

### 5.2 Exchange

```protobuf
message PairRequest {          // B -> A
  bytes  identity_public_key  = 1;  // 32 bytes
  bytes  privilege_public_key = 2;  // 32 bytes, D-20
  string display_name         = 3;
  string platform             = 4;
  bytes  nonce                = 5;  // 32 random bytes
  uint32 protection_tier      = 6;
}

message PairResponse {         // A -> B, same shape
  bytes  identity_public_key  = 1;
  bytes  privilege_public_key = 2;
  string display_name         = 3;
  string platform             = 4;
  bytes  nonce                = 5;
  uint32 protection_tier      = 6;
}
```

Both sides then compute the same **short authentication string**:

```
transcript = "openair-pair-v1"
          || min(idA, idB) || max(idA, idB)          // identity public keys
          || min(privA, privB) || max(privA, privB)  // privilege public keys
          || nonceA || nonceB                        // initiator's nonce first

SAS = decimal( SHA-256(transcript)[0:4] ) mod 1000000   // six digits, zero-padded
```

Sorting the key pairs makes the transcript independent of role, so both sides
derive it identically without agreeing on who is "first". Including both nonces
prevents replay of a previous pairing.

Each device displays its SAS. **The user MUST confirm on both devices that the
six digits match.** A man-in-the-middle would have to produce two different
key exchanges, so its two SAS values differ and the comparison fails.

```protobuf
message PairConfirm { bool accepted = 1; }
```

Pairing completes only when both peers send `accepted = true`. Both keys are
then pinned, and the peer is recorded at level **Trusted**. Promotion to Owned
is a separate, deliberate act on the target device (PRD R3) and never happens
during pairing.

Implementations MUST NOT offer a "skip verification" path. The SAS comparison is
the entire security of the pairing.

---

## 6. Authorisation

Trust levels are **Trusted** (per-session or per-capability consent) and
**Owned** (unattended access). Capabilities declare a required level; the
session layer checks it before the message reaches the capability (HLD §3.4).

Owned-level requests MUST carry proof of possession of the **privilege key**,
which is only available while an unlock session is live (D-18, D-19, D-20).

```protobuf
message AuthProof {
  bytes  nonce      = 1;  // 32 random bytes, fresh per request
  int64  issued_at  = 2;  // unix milliseconds
  bytes  signature  = 3;  // Ed25519 over the string below
}
```

```
signed = "openair-owned-v1"
      || target_device_id      // the peer being asked, binding the proof to it
      || capID || msgType      // binding it to this operation
      || nonce || issued_at
```

The verifier MUST reject the request unless all hold:

1. The signature verifies against the peer's **pinned privilege public key**.
2. `target_device_id` equals the verifier's own DeviceID. This is what stops a
   proof captured by one peer being replayed against another.
3. `issued_at` is within **±60 seconds** of local time.
4. `nonce` has not been seen before within that window.

Binding to `capID` and `msgType` means a proof for a file read cannot be reused
to start a screen mirror.

**Note on token scope.** Whether one local unlock grants access to every paired
peer or only to one is a local policy question (D-18) and has no wire effect: a
proof is always bound to a single target and a single operation.

### 6.1 Revocation

```protobuf
message Revoke {
  TrustLevel new_level = 1;  // UNPAIRED discards both pinned keys
  string     reason    = 2;
}
```

`TrustLevel` is a shared ladder — `UNPAIRED` < `TRUSTED` < `OWNED` — used by both
`Revoke` and `CapabilityGrant`. An earlier revision gave each a bare `uint32`
with a separate implied scale, which is how a revoke and a grant end up
disagreeing about what level 1 means.

Sent on the control stream. On receipt the peer MUST immediately stop honouring
operations above `new_level` and MUST abort in-flight ones that exceed it.
Demotion from Owned to Trusted without unpairing exists because two keys make it
meaningful (D-20); `new_level = 0` additionally requires discarding both pinned
keys. Revocation takes effect mid-session (PRD R5).

---

### 6.2 Consent at Trusted level

Owned peers act without per-session approval — that is what Owned means. **Trusted
peers, which is the default at pairing, require consent** (PRD R3), and this is
the more common path.

```protobuf
message ConsentRequest {
  string request_id     = 1;
  uint32 cap_id         = 2;
  string operation      = 3;  // human-readable, shown in the prompt verbatim
  uint32 requested_scope = 4; // 1 = once, 2 = session, 3 = persistent
}

message ConsentResponse {
  string request_id = 1;
  bool   granted    = 2;
  uint32 scope      = 3;  // MAY be narrower than requested, MUST NOT be wider
}
```

The model is hybrid, matching how platform app permissions already behave:
capabilities granted at pairing or later (§6.4) are persistent and prompt
nothing; anything ungranted prompts once per session.

- `operation` MUST be shown to the user as written. It is the only thing telling
  them what they are approving, so implementations MUST NOT substitute a generic
  string.
- Granted `scope` MUST NOT exceed `requested_scope`. A peer asking for `once`
  cannot be handed `persistent`.
- A denial MUST NOT be re-requested for the same capability within the session.
  Prompt fatigue is an attack, not an inconvenience.
- `scope = 3` MUST be recorded in the trust store, so it survives restarts and
  appears in the paired-device list as something revocable.

### 6.3 Session lifecycle

PRD R4 requires the accessed device to show a visible indicator, keep a local
session log, and let a local user kill a session instantly.

```protobuf
message SessionAnnounce {
  string session_id      = 1;
  repeated uint32 cap_ids = 2;  // capabilities this session intends to use
  string purpose         = 3;
  bool   owned_level     = 4;
}

message SessionEnd  { string session_id = 1; string reason = 2; }
message SessionKill { string session_id = 1; }  // accessed device -> initiator
```

An initiator MUST send `SessionAnnounce` before its first Owned-level operation
and before any use of `input` or `mirror`. The accessed device MUST display a
visible indicator for the lifetime of any session naming those capabilities, and
MUST log announce, end, kill and every authentication event locally — auth events
originate on the initiator (D-18), so both ends keep a record and neither log is
sufficient alone.

**`SessionKill` is a courtesy, not the enforcement.** A local user killing a
session takes effect by the accessed device refusing further operations and
resetting the relevant streams. The message tells a well-behaved peer why; a
misbehaving peer cannot ignore its way out, because the enforcement is local.

### 6.4 Granting capabilities

`Revoke` (§6.1) narrows access. Granting widens it, and is what makes the paired
device list manageable rather than write-once (PRD R5).

```protobuf
message CapabilityGrant {
  repeated uint32 cap_ids = 1;
  uint32 level            = 2;  // 1 = Trusted, 2 = Owned
}
```

Sent by the device *granting* access, on its own initiative — never in response
to a request, so a peer cannot ask to be promoted. Promotion to Owned remains a
deliberate act on the granting device (PRD R3). Grants take effect immediately.

### 6.5 Expiry and work in flight

When an unlock session expires (D-18), the peer's privilege key becomes
unavailable and further Owned requests fail with `AUTH_EXPIRED`.

Operations already running are **not** aborted: they were authorised when they
began, and destroying a 20 GB transfer twelve minutes from completion serves
nobody. Implementations MUST therefore:

- reject **new** Owned operations at expiry;
- permit in-flight ones to continue for a **grace of at most one hour**, after
  which they are aborted regardless. Without the cap, starting a long operation
  just before expiry would extend access indefinitely, which is precisely what
  the timer exists to prevent;
- notify the user **15 minutes before** expiry, so a long transfer can be
  extended deliberately rather than discovered broken.


## 7. Flow control

### 7.1 Quiesce

Priority classes are enforced above the transport, not by it — quic-go exposes
no stream prioritisation (D-24). A peer expecting sustained high-bitrate traffic
asks the other to throttle bulk:

```protobuf
message QuiesceRequest {
  uint32 floor_bytes_per_sec = 1;  // 0 means "stop entirely"; SHOULD be non-zero
  string reason              = 2;  // human-readable, for the session log
}

message QuiesceRelease {}
```

The **sender of the bulk data** is the party that throttles, which is not
necessarily the party starting the high-bandwidth capability — hence a wire
message rather than local policy.

Receivers SHOULD throttle to a floor rather than stopping outright; a
multi-hour mirror session would otherwise stall a large transfer indefinitely
(D-24). Requesters MUST send `QuiesceRelease` when the capability ends, and
receivers SHOULD resume automatically if no traffic for that capability is seen
for 30 seconds, so a crashed peer cannot leave bulk throttled forever.

Quiesce is not instantaneous: up to a congestion window is already in flight.

### 7.2 Path quality hints

```protobuf
message PathInfo {
  uint32 rtt_ms              = 1;
  uint64 bandwidth_bytes_sec = 2;
  PathClass path_class       = 3;  // lan | punched | relayed
}
```

Advisory. Capabilities MAY adapt quality to it and MUST remain functional
without it — capability parity across paths is required, quality parity is not
(PRD R8).

### 7.3 Protection tier

`protection_tier` in `Hello` and the pairing messages reports how the peer
protects its privilege key (D-21): `1` keystore or TPM, `2` passphrase via
Argon2id, `3` unprotected. A device offering tier 3 MUST NOT be granted Owned
level; implementations MUST surface this rather than degrading silently.

---

## 8. Capability: `files` (capID 1)

Descends directly from v1.0's engine (D-4). Bulk priority.

### 8.1 Offer

```protobuf
message TransferOffer {
  string transfer_id  = 1;  // random, 16 bytes base32
  repeated FileMeta files = 2;
  uint64 total_bytes  = 3;
  uint32 stream_count = 4;  // data streams the sender intends to open
  uint64 chunk_size   = 5;
}

message FileMeta {
  string path        = 1;  // relative, forward slashes; MUST NOT escape the root
  uint64 size        = 2;
  int64  modified_at = 3;
  bytes  sha256      = 4;  // whole-file digest, optional; empty if not computed
}

message TransferAccept {
  string transfer_id     = 1;
  bool   accepted        = 2;
  repeated uint64 have_chunks = 3;  // chunk indices already verified, for resume
}
```

A receiver MUST reject any `path` that is absolute, contains `..`, or resolves
outside the destination root. Path traversal is the obvious attack on a file
transfer protocol and the check belongs here, not in the UI.

Auto-accept is permitted for Owned peers (PRD R11); Trusted peers require
explicit consent.

### 8.2 Data streams

Each data stream opens with one enveloped message and then carries **raw chunk
frames only**:

```protobuf
message StreamInit {
  string transfer_id = 1;
}
```

### 8.3 Chunk frame — raw, not protobuf

```
 0                                 8              12
 +---------------------------------+--------------+
 | offset (u64)                    | size (u32)   |
 +---------------------------------+--------------+
 | data — `size` bytes                            |
 +------------------------------------------------+
```

Byte-identical to v1.0's 12-byte header. Header and payload SHOULD be written in
a single call; v1.0 issued three writes per chunk, which measurably slowed it.

The receiver writes each chunk at its offset (`WriteAt`), so chunks may arrive in
any order across any number of streams.

### 8.4 Integrity and completion

```protobuf
message ChunkManifest {
  string transfer_id = 1;
  uint64 chunk_size  = 2;
  repeated bytes chunk_sha256 = 3;  // one 32-byte digest per chunk, in order
}

message TransferComplete {
  string transfer_id = 1;
  bool   ok          = 2;
  repeated uint64 failed_chunks = 3;
}
```

The manifest is sent on the control stream before or during transfer. The
receiver MUST verify each chunk against it and MUST report mismatches in
`failed_chunks` rather than silently accepting corrupt data. Digests live in the
manifest rather than in the chunk frame so the hot path stays 12 bytes.

Resume: on reconnect the sender re-offers the same `transfer_id`; the receiver
returns verified chunk indices in `have_chunks` and the sender skips them
(PRD R13).

### 8.5 Cancellation and progress

```protobuf
message TransferCancel {
  string transfer_id     = 1;
  string reason          = 2;
  bool   discard_partial = 3;  // default false — partial data is kept for resume
}

message TransferProgress {
  string transfer_id    = 1;
  uint64 bytes_received = 2;
  uint64 chunks_verified = 3;
}
```

Either side may cancel (PRD R13). The receiver keeps partial data and its
verified-chunk set unless `discard_partial` is set, because a cancel on a flaky
link is usually a prelude to retrying, and resume is the feature that makes that
cheap. Senders SHOULD offer discard explicitly rather than defaulting to it.

`TransferProgress` flows from receiver to sender at roughly 1 Hz. Without it a
sender cannot display receiver-side progress at all, since bytes written to the
network are not bytes committed to disk. Speed and ETA are derived by the
sender; they are deliberately not on the wire, because they are presentation.

---

## 9. Capability: `clipboard` (capID 2)

```protobuf
message ClipboardPush {
  string mime    = 1;  // "text/plain; charset=utf-8" initially
  bytes  content = 2;
  int64  origin_ts = 3;
  string origin_tag = 4;  // suppresses echo loops in auto-sync mode
}
```

Interactive class. Manual push is the guaranteed path on every platform;
automatic sync is opt-in and best-effort where OS policy restricts background
clipboard reads (PRD R19). Content MUST NOT be persisted by relays or logged in
plaintext (PRD R20). Receivers SHOULD cap accepted content and reject oversized
pushes rather than buffering them.

---

## 10. Error codes

Carried in QUIC `CONNECTION_CLOSE` (connection-fatal) or `STOP_SENDING` /
`RESET_STREAM` (operation-fatal).

| Code | Name | Fatal to | Meaning |
|---|---|---|---|
| 0x00 | `NO_ERROR` | — | Normal closure |
| 0x01 | `PROTOCOL_VIOLATION` | connection | Malformed framing, or a claimed identity that does not match the TLS key |
| 0x02 | `UNKNOWN_VERSION` | connection | Unsupported envelope version |
| 0x03 | `MESSAGE_TOO_LARGE` | connection | Envelope `length` above the cap |
| 0x04 | `NOT_PAIRED` | connection | Peer key is not pinned |
| 0x05 | `KEY_MISMATCH` | connection | Pinned key differs — requires re-pairing, never retry |
| 0x06 | `UNAUTHORISED` | operation | Trust level insufficient, or `AuthProof` failed |
| 0x07 | `AUTH_EXPIRED` | operation | Unlock session expired; the initiator must re-authenticate |
| 0x08 | `REJECTED` | operation | User declined |
| 0x09 | `CAPABILITY_UNAVAILABLE` | operation | Not negotiated, or disabled |
| 0x0a | `RESOURCE_EXHAUSTED` | operation | Out of disk, memory, or stream budget |
| 0x0b | `INTEGRITY_FAILURE` | operation | Chunk digest mismatch |

`KEY_MISMATCH` MUST NOT be retried automatically. It means either a re-installed
peer or an attack, and the two are indistinguishable to the protocol.

---

## 11. Capability: `remotefs` (capID 3) — Phase 3

Browse and read another device's filesystem without transferring whole files
(PRD R14–R16). Deliberately a **dumb server and a smart client**: the source
serves byte ranges and does not know it is serving a video. Read-ahead,
buffering and caching are entirely the client's business.

Bulk priority for reads; interactive for metadata.

### 11.1 Metadata

```protobuf
message StatRequest  { string path = 1; }
message StatResponse { FileStat stat = 1; }
message ListRequest  { string path = 1; uint32 offset = 2; uint32 limit = 3; }

message FileStat {
  string path        = 1;
  uint64 size        = 2;
  int64  modified_at = 3;
  bool   is_dir      = 4;
  uint32 mode        = 5;   // POSIX permission bits, best-effort on Windows
  string mime        = 6;   // sniffed by the source, advisory
}

message ListResponse {
  repeated FileStat entries = 1;
  bool   truncated = 2;   // more remain; re-request with a higher offset
}
```

Listing is paginated because a directory of 100k entries must not become a
16 MiB envelope. Roots exposed for browsing are configured on the source; a
request escaping them MUST be refused with `UNAUTHORISED`, applying the same
traversal rules as §8.1.

### 11.2 Range reads

The client opens a stream, sends one `ReadRequest`, and the source replies with
a `ReadResponse` envelope followed by **raw bytes**. One request per stream, so
several reads proceed concurrently without head-of-line blocking between them.

```protobuf
message ReadRequest  { string path = 1; uint64 offset = 2; uint64 length = 3; }
message ReadResponse { uint64 offset = 1; uint64 length = 2; bool eof = 3; }
```

`length` in the response MAY be shorter than requested — at end of file, or
because the source chose a smaller quantum. Clients MUST handle short reads.

This is the primitive everything else rests on: a media player seeking to the
middle of a 40 GB file simply issues a read at that offset (PRD R16).

### 11.3 Thumbnails

```protobuf
message ThumbRequest  { string path = 1; uint32 max_dimension = 2; }
message ThumbResponse { string mime = 1; bytes image = 2; }
```

Generated on the source, so browsing a folder of RAW photos does not transfer
them. Sources SHOULD cache thumbnails and MUST bound generation work.

### 11.4 Client-side behaviour (non-normative but load-bearing)

Read-ahead window SHOULD adapt to `PathInfo` — roughly RTT × observed bitrate.
An LRU disk cache MAY be kept; if it is, it MUST be encrypted at rest and
size-capped, because it holds someone else's files (PRD K8).

---

## 12. Capability: `notifications` (capID 4) — Phase 3

Mirrors notifications from a source device, usually a phone, to sinks (PRD
R21–R23). Interactive priority.

```protobuf
message Posted {
  string key         = 1;   // opaque, stable, source-assigned
  string app_id      = 2;   // package name or equivalent
  string app_name    = 3;
  string title       = 4;
  string body        = 5;
  bytes  icon_png    = 6;   // small; sources SHOULD cap to 64x64
  int64  posted_at   = 7;
  string category    = 8;   // "msg" | "call" | "alarm" | "progress" | ""
  repeated NotificationAction actions = 9;
}

message NotificationAction {
  string action_id   = 1;
  string label       = 2;
  bool   accepts_text = 3;  // inline reply
}

message Removed      { string key = 1; }
message Dismiss      { string key = 1; }   // sink -> source
message ActionInvoke { string key = 1; string action_id = 2; string text = 3; }
```

**Filtering happens on the source** (PRD R22). A per-app allowlist is evaluated
before `Posted` is constructed, so excluded content never crosses the wire and
never reaches a relay. There is deliberately no filter-configuration message
here: filtering is local policy, and putting it on the wire would mean the
filtered content had already left the device.

`Dismiss` flowing back gives dismiss-on-one-dismisses-everywhere. Sinks MUST
tolerate a `Removed` for a key they never saw.

Desktop-to-desktop forwarding uses the same messages in the same direction —
there is nothing phone-specific in the format (PRD R23).

---

## 13. Capability: `input` (capID 5) — Phase 4

Keyboard, pointer and touch injection for remote control (PRD R25). **Interactive
priority, carried on QUIC datagrams**, so input is never queued behind a stream.

Datagram payload, raw rather than protobuf, because these are tiny and frequent:

```
 0        1        2                                 6
 +--------+--------+---------------------------------+
 | capID  | kind   | seq (u32)                       |
 +--------+--------+---------------------------------+
 | event body — kind-dependent, see below            |
 +----------------------------------------------------+
```

The leading `capID` byte demultiplexes datagrams, which have no stream to
identify them by.

**`input` is the one capability with no protobuf messages at all**, and that is
deliberate: these events are tiny and frequent, so protobuf framing would cost
more than the payload, and a fixed layout demultiplexes without allocating.

| kind | body |
|---|---|
| `0x01` key | `usage(u16)` `down(u8)` `modifiers(u8)` |
| `0x02` pointer-move | `x(i32)` `y(i32)` `absolute(u8)` |
| `0x03` pointer-button | `button(u8)` `down(u8)` |
| `0x04` scroll | `dx(i32)` `dy(i32)` `precise(u8)` |
| `0x05` touch | `id(u8)` `phase(u8)` `x(i32)` `y(i32)` |

Keys use **USB HID usage codes**, not platform keycodes or characters. This is
the only representation that maps cleanly onto `SendInput`, XTest/libei and
Android's `InputManager` without the protocol taking a position on layout —
layout is applied by the target OS, which is the only party that knows it.

**Drop-stale semantics.** Pointer-move and scroll are latest-wins: a receiver
that sees `seq` lower than the highest already applied for that kind MUST
discard it. Key and button events are state transitions and MUST NOT be dropped
on that basis; a lost key-up would leave a key stuck, so receivers SHOULD apply
a safety release for any key held longer than 5 seconds with no traffic.

Absolute coordinates are in the target's screen space, normalised by the
`mirror` session's declared dimensions when one is active.

---

## 14. Capability: `mirror` (capID 6) — Phase 4

**Provisional. ADR-4 (D-9) is open.** The framing below reflects the current
leaning — stream-per-frame with `RESET_STREAM` — which D-24 made favourite by
removing the scheduling argument for datagrams. It MUST be validated by the D-9
spike before implementation, and this section is expected to change.

### 14.1 Session control

```protobuf
message MirrorStart {
  uint32 width        = 1;
  uint32 height       = 2;
  uint32 fps          = 3;
  string codec        = 4;   // "h264" initially; "hevc", "av1" later
  uint32 max_bitrate  = 5;   // bytes/sec
  uint32 display_id   = 6;   // which screen, when several
}

message MirrorStop    {}
message RequestIDR    {}                       // sink -> source, after loss
message BitrateHint   { uint32 bytes_per_sec = 1; }
message MirrorStats   {                        // sink -> source, ~1 Hz
  uint32 frames_decoded = 1;
  uint32 frames_dropped = 2;
  uint32 decode_ms_p95  = 3;
  uint32 jitter_ms      = 4;
}
```

`MirrorStart` triggers the quiesce of §7.1 automatically — the source requests
bulk throttling for the duration of the session, and releases it on
`MirrorStop`.

### 14.2 Frame transport (provisional)

Each encoded frame is sent on its **own bidirectional stream**, opening with:

```protobuf
message FrameHeader {
  uint64 seq         = 1;
  int64  captured_at = 2;   // source monotonic microseconds
  bool   keyframe    = 3;
}
```

followed by the raw encoded frame bytes, then a clean stream close.

If a frame becomes stale before it is fully sent — a newer frame is ready, or
its deadline passes — the source MUST `RESET_STREAM` it. The sink then stops
waiting and the frame is abandoned rather than retransmitted, which is the
property realtime video actually needs.

Why not datagrams, given they are the conventional choice: measurement (D-17)
found no latency advantage for small messages, and datagrams are MTU-bound, so a
200 KB keyframe becomes roughly 170 fragments requiring application-level
fragmentation, reassembly and partial-loss detection — against a send queue only
32 entries deep whose overflow is a silent discard. Streams provide
fragmentation, reassembly and flow control for free. The open question the spike
must answer is whether stream churn at 60 fps costs more than that saves.

Screen capture on Android requires per-session user consent by OS policy
(PRD K6); fully unattended mirroring of an Android target may be impossible and
this protocol does not attempt to work around it.

---

## 15. Discovery — Phase 1 (LAN), Phase 2 (WAN)

### 15.1 mDNS

Service `_openair._udp`, TXT records:

| Key | Value |
|---|---|
| `id` | DeviceID (§2) |
| `v` | highest supported protocol version |
| `port` | QUIC port |
| `n` | display name, UTF-8 |

Note the change from v1.0's `_openair._tcp`: v2 is QUIC, therefore UDP.

### 15.2 Unicast fallback

Some networks suppress multicast. Peers MAY additionally broadcast a signed
announce to the subnet broadcast address, and MAY probe known-last-good
addresses directly. Discovery emits candidates; **it never dials** (HLD §3.2).

Discovery is a hint, not an authority: a discovered peer is still authenticated
by pinned key (§2), so a hostile announce achieves nothing beyond a failed
handshake.

---

## 16. Rendezvous — Phase 2

A rendezvous server maps a DeviceID to current endpoints so paired devices can
find each other across networks (PRD R7). Self-hostable, and never a required
third party.

```protobuf
message Registration {
  string device_id       = 1;
  repeated string endpoints = 2;  // "ip:port", reflexive and local
  string relay_home      = 3;     // relay this device is reachable through
  int64  issued_at       = 4;
  int64  expires_at      = 5;
  bytes  signature       = 6;     // Ed25519 by the identity key
}

message LookupRequest  { string device_id = 1; }
message LookupResponse { Registration registration = 1; bool found = 2; }
```

```
signed = "openair-rendezvous-v1" || device_id || endpoints (in order)
      || relay_home || issued_at || expires_at
```

The server MUST verify the signature against the registering key before storing,
so only the key holder can move a device's endpoints. It MUST reject
registrations whose `expires_at` is more than 10 minutes out, forcing heartbeats
and keeping stale entries short-lived.

**What a rendezvous operator learns**, stated plainly for the threat model
(PRD R29): which DeviceIDs exist, their IP endpoints, when they are online, and
who looks up whom — a social graph of the user's own devices. It learns no
session content, because it carries none. Users who consider this significant
should self-host, which is why the design keeps it trivially self-hostable.

---

## 17. Relay — Phase 2

A DERP-style forwarder for paths where direct connectivity fails (PRD R8). It
moves ciphertext between two peers and holds no keys.

Listens on TLS/443 so it survives restrictive networks. Clients authenticate by
proving possession of their identity key:

```protobuf
message RelayHello     { string device_id = 1; bytes nonce = 2; }
message RelayChallenge { bytes server_nonce = 1; }
message RelayAuth      { bytes signature = 1; }   // over both nonces
```

After authentication a client has a mailbox keyed by DeviceID. Frames are:

```
 0                        16                     20
 +------------------------+----------------------+
 | dst_device_id (16)     | length (u32)         |
 +------------------------+----------------------+
 | payload — an opaque QUIC datagram             |
 +-----------------------------------------------+
```

The relay MUST NOT inspect or modify the payload, MUST NOT deliver to a DeviceID
that has not authenticated, and SHOULD rate-limit per source. Because the
payload is a complete QUIC packet, end-to-end encryption is unchanged when a
path is relayed — the relay is a network element, not a participant (PRD R27).

**What a relay operator learns:** which DeviceIDs talk to each other, when, and
how much. Not content. Same self-hosting argument as §16.

---

## 18. Connection establishment — Phase 2

One QUIC connection per peer, over whichever path is available, upgraded live
(D-6, HLD §3.3).

1. **Gather candidates** — LAN addresses from §15, reflexive addresses from
   STUN, and the relay path, which is always available.
2. **Start on the relay immediately.** The session is usable within about one
   round trip to the relay rather than waiting for a punch to succeed or fail.
3. **Race direct candidates in parallel** — LAN dial, plus coordinated UDP hole
   punching using the messages below.
4. **Migrate** to a better path when one succeeds. QUIC connection IDs make this
   native: streams do not break and transfers do not restart (PRD R9).
5. **Re-race** on network-change events, and fall back to the relay if a direct
   path dies.

```protobuf
message PunchRequest {
  string target_device_id = 1;
  repeated string candidates = 2;
  bytes  punch_token = 3;   // 16 random bytes, echoed to correlate attempts
  int64  start_at    = 4;   // unix ms; both sides spray from this instant
}

message PunchReady {
  repeated string candidates = 1;
  bytes  punch_token = 2;
}
```

Signalling travels over the rendezvous server or an existing relay session.
`start_at` synchronises the spray, which is what makes symmetric-NAT traversal
work at all often enough to matter; both sides MUST begin within about 50 ms of
it and MUST NOT rely on their clocks being better than that.

Path selection is by measured RTT with hysteresis, so a marginally faster path
does not cause migration flapping. Capabilities are never told which path they
are on — only a `PathInfo` hint (§7.2), so that capability parity across paths
stays structural rather than a per-feature obligation (PRD R8).

---

## Appendix A — At-rest key format (normative, not wire)

The privilege key is stored encrypted (D-19). This format is versioned
independently of the wire protocol.

```
magic        "OAKEY\0"          6 bytes
version      u8                 1 = this format
kdf          u8                 0 = none (platform keystore), 1 = Argon2id
argon_time   u32                iterations       (0 when kdf = 0)
argon_memory u32                KiB              (0 when kdf = 0)
argon_lanes  u8                 parallelism      (0 when kdf = 0)
salt_len     u8
salt         salt_len bytes
nonce        24 bytes           XChaCha20-Poly1305
ct_len       u32
ciphertext   ct_len bytes       sealed Ed25519 private key
```

- AEAD is **XChaCha20-Poly1305**. The header from `magic` through `salt` MUST be
  authenticated as associated data, so KDF parameters cannot be downgraded by an
  attacker who can edit the file.
- Argon2id parameters are stored rather than assumed, so cost can be raised later
  without stranding existing installs (D-19).
- With `kdf = 0` the key-encryption key comes from the platform keystore and no
  passphrase material is present.
- Rate limiting on the passphrase path protects the interactive path only. It
  cannot protect against offline attack, which is why tier 1 exists (D-21).

---

## Appendix B — Capability IDs

| ID | Name | Phase | Status |
|---|---|---|---|
| 0 | `control` | 1 | Specified — §4, §6, §7 |
| 1 | `files` | 1 | Specified — §8 |
| 2 | `clipboard` | 1 | Specified — §9 |
| 3 | `remotefs` | 3 | Specified — §11 |
| 4 | `notifications` | 3 | Specified — §12 |
| 5 | `input` | 4 | Specified — §13 |
| 6 | `mirror` | 4 | **Provisional** — §14, pending D-9 |
| 7 | `daemon` | 1 | **Local IPC only** — never on a network session (D-51) |

IDs 8–127 are reserved for future core capabilities. 128–255 are available for
experiments and MUST NOT appear in a release.

`daemon` is in this table because the local link between `openaird` and its
shells carries the same envelope as the network protocol (D-29), so its capID
has to be reserved against a future network capability taking it. It never
travels between devices: a peer sending capID 7 over QUIC reaches no registered
handler and is ignored under §3.1. Its messages are in
`proto/openair/v1/daemon.proto` and are not part of the interoperable protocol.

---

## Appendix C — Deliberately unspecified

Named so their absence is visibly a decision rather than an oversight:

- **Compression.** Bulk transfer of already-compressed media is the common case,
  and compression would add CPU to a path already CPU-bound on QUIC (D-4). If it
  is added it belongs per-file, negotiated in `TransferOffer`, never blanket.
- **Codec negotiation beyond a string.** §14 carries a codec name and nothing
  about profiles, levels or hardware capability. It needs a real capability
  exchange before Phase 4, and that waits on D-9.
- **Multi-device coordination.** No consensus, no replication, no N-way clipboard
  with conflict rules. Every capability here is strictly pairwise (D-11).
- **Group or third-party sharing.** Out of scope by PRD NG2. There is no message
  in this protocol addressed to anyone but a paired device of the same user.
- **Transcode-on-source for `remotefs`.** PRD R17 lists it as a stretch; raw
  range streaming (§11.2) is the baseline and works without it.
- **Key rotation without re-pairing.** Pinned keys mean a rotated key hard-fails
  by design (§2, D-7). A rotation ceremony could be added later; it is not
  needed for any Phase 1–4 capability.

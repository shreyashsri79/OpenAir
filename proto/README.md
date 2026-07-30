# OpenAir wire schemas

Normative source is `docs/PROTOCOL.md`. These files are its executable form —
where they disagree, the spec is right and these are a bug, except where a file
comment records a spec defect found while writing it.

## Layout

`openair/v1/` — one file per capability, plus shared types.

| File | Covers | Phase |
|---|---|---|
| `common.proto` | shared enums: capability IDs, trust levels, protection tiers, consent scopes, path classes | — |
| `session.proto` | `Hello`, capability negotiation, control message types | 1 |
| `pairing.proto` | TOFU pairing exchange | 1 |
| `auth.proto` | `AuthProof`, revoke, grant, consent, session lifecycle | 1 |
| `flow.proto` | quiesce, path hints | 1 |
| `files.proto` | transfer offer, manifest, cancel, progress | 1 |
| `clipboard.proto` | clipboard push | 1 |
| `infra.proto` | rendezvous, relay auth, hole punching | 2 |
| `remotefs.proto` | stat, list, range reads, thumbnails | 3 |
| `notifications.proto` | notification mirroring | 3 |
| `mirror.proto` | screen mirroring — **provisional**, ADR-4 (D-9) is open | 4 |

## What is deliberately not here

**`input` (capID 5) has no protobuf messages.** It rides QUIC datagrams with a
raw six-byte header and a kind-dependent body, specified in `PROTOCOL.md` §13.
Input events are tiny and frequent — protobuf framing would cost more than the
payload, and the fixed layout demultiplexes without allocation.

**Bulk data frames are not protobuf either.** A `files` data stream opens with
one `StreamInit` envelope and then carries raw 12-byte-header chunk frames,
inherited byte-for-byte from v1.0. The hot path never parses.

## Two encoding traps

**Enum values are not wire values.** proto3 reserves 0 for `UNSPECIFIED`, so
every enum here is offset by one from the numbering in `PROTOCOL.md`. The
envelope's capID byte for `files` is 0x01 while `CAPABILITY_ID_FILES` is 2.
Encoders must convert rather than casting.

**The envelope itself is not protobuf.** It is a fixed 8-byte header — version,
capID, msgType, length — wrapping a protobuf payload, so demultiplexing costs no
allocation and a malformed message cannot be mistaken for a valid one of another
type.

## Regenerating

```bash
buf lint            # STANDARD ruleset
buf breaking --against '.git#branch=main'
buf generate        # writes internal/wire/, which is committed (D-28)
```

Generated code is committed so a plain `go build` works on a fresh clone with
only the Go toolchain, and so wire-format changes are visible in review — which
is exactly where they should be caught.

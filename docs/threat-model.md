# OpenAir 2.0 — Threat Model

**Status:** Draft 0.1 · **Satisfies:** PRD R29 · **Companion docs:** PRD v2, HLD, `PROTOCOL.md`, `decision-tree.md`

This document is assembly, not invention. Every property claimed here is already
decided somewhere — in `PROTOCOL.md`, or in a `decision-tree.md` entry — and is
cited inline. Where the existing decisions leave a question genuinely open, this
document says so and does not supply a mitigation that nobody chose. §7 collects
those, and it is the section a reviewer should read first.

Scope is Phases 1–4 as specified in `PROTOCOL.md`. §14 (`mirror`) is provisional
pending D-9, so its media path is modelled only as far as it is specified.

---

## 1. What the system is defending

OpenAir is one user's own devices, pairwise (PRD NG2, Appendix C). There is no
multi-user model, no organisation, no sharing with strangers. That is what makes
this threat model tractable and also what bounds it: nothing here defends one
principal against another *inside* the same trust domain, because the design has
only one principal.

The security posture in one sentence: **possession of a device's keys is
possession of that device's authority**, moderated by a second key that is
gated behind user presence for the dangerous half of the feature set (D-20).

---

## 2. Assets

| Asset | Where it lives | Consequence of compromise |
|---|---|---|
| **Identity key** (Ed25519) | Every device, warm and ungated by construction (D-20) | Impersonate the device to every peer that pinned it; terminate TLS as it; complete handshakes; exercise everything granted at Trusted level — inbound transfer offers, clipboard push, notification mirroring — subject to the far end's consent rules (§6.2) |
| **Privilege key** (Ed25519) | Every device above tier 3; encrypted at rest (D-19, Appendix A) | Mint `AuthProof` signatures (§6) and therefore drive Owned-level operations: remote filesystem browse, screen mirror, remote input, unattended pull |
| **Trust store** | Every device | Pinned peer public keys, trust levels, persistent capability grants, protection tiers, `auth_policy` (D-30's field table). Write access is equivalent to granting yourself Owned — see §7.1 |
| **Unlock credential** | TPM/keystore (tier 1) or a passphrase (tier 2), per D-21 | Unseals the privilege key |
| **User content in transit** | QUIC streams and datagrams | Files, clipboard, notification bodies, screen frames, keystrokes |
| **User content at rest that OpenAir creates** | `remotefs` LRU disk cache (§11.4), local session log (§6.3) | The cache holds another device's files; the log holds a record of who accessed what and when |
| **Presence and endpoint metadata** | Rendezvous registrations (§16), mDNS TXT records (§15.1) | Device existence, IP endpoints, online times, who talks to whom |

The two-key split (D-20) exists because one key cannot be both sealed behind
user presence and warm enough to keep an unattended machine reachable — PRD G5
and S3 need the second property, D-18 needs the first, and they are irreducibly
opposed. Only the privilege key is gated. **The identity key is never gated, on
any platform, at any tier.** Everything the identity key alone can do is
therefore available to anyone holding the device, always.

---

## 3. Trust boundaries

**B1 — The pinned-key boundary (network).** A peer is trusted iff its raw public
key matches the value pinned at pairing (§2, D-7). No CA, no chain, no hostname
check. A mismatch MUST hard-fail and surface as a re-pair prompt (§2). This is
the only network-facing authentication in the system; discovery (§15), rendezvous
(§16) and relay (§17) are all *outside* it and are treated as hostile by default.

**B2 — The Owned boundary (within an authenticated session).** Crossing from
Trusted to Owned requires an `AuthProof` signed by the pinned privilege key,
bound to the verifier's own DeviceID, to `capID`/`msgType`, to a fresh nonce and
to a ±60 s timestamp window (§6). The verifier enforces all four. This boundary
sits *inside* an already-authenticated TLS session, which is why stealing the
identity key does not get you past it.

**B3 — The local IPC socket.** `openaird` and its tray UI/CLI speak the same
protobuf envelope over a unix socket or named pipe (D-29, superseding the HLD §2
gRPC choice). There is no authentication on this channel. **Any process that can
open the socket drives the daemon** — D-29 states this and makes filesystem
permissions and, on Windows, a named-pipe ACL a security requirement rather than
hygiene. Those permissions are per-user, so on a desktop the boundary is the OS
user account, not the process. Android has no IPC boundary at all: the core runs
in-process via gomobile (D-10, D-31), so the boundary there is the app sandbox.

**B4 — The at-rest boundary.** Appendix A defines an authenticated, versioned
container for the privilege key: XChaCha20-Poly1305 with the KDF header
authenticated as associated data, so Argon2id parameters cannot be downgraded by
an attacker who can edit the file. This boundary covers the privilege key and
(per §11.4) the `remotefs` cache. It does **not** currently cover the trust store
or the identity key — see §7.1 and §7.2.

**B5 — The user-presence boundary.** Tier 1 puts this in hardware: keystore or
TPM 2.0, with attempt limits the hardware enforces (D-21). Tier 2 puts it in a
passphrase through Argon2id, which is not brute-force-resistant in the same
sense, only expensive. Tier 3 has no boundary here at all, which is exactly why
a tier-3 device MUST NOT be granted Owned (§7.3, D-21).

---

## 4. Adversaries

### 4.1 Passive network observer

Sees: QUIC packets. ALPN `openair/1` in the clear on the initial handshake
(§1), so OpenAir traffic is trivially fingerprintable. Packet sizes, timing,
volumes, and the endpoints of each path. On a LAN, mDNS TXT records in the clear:
DeviceID, protocol version, port, display name (§15.1).

Does not get: any content. TLS 1.3 only (§1); earlier versions MUST be refused.
Key exchange is ephemeral, so recording ciphertext today and stealing the
identity key tomorrow does not retroactively decrypt it.

Residual: traffic analysis. Nothing in `PROTOCOL.md` specifies padding or cover
traffic. `input` is carried as datagrams (§13) at human keystroke cadence, and
`mirror` frame sizes track screen activity; both are legible to an observer as
patterns even though the bytes are opaque.

### 4.2 Active on-path attacker

Cannot impersonate a paired peer: the pinned raw public key is checked in
`VerifyPeerCertificate` and a mismatch is a hard failure (§2, D-7). Cannot
downgrade the transport: TLS 1.3 only, non-matching ALPN MUST be rejected (§1).
Cannot replay an Owned authorisation: `AuthProof` binds `target_device_id`, so a
proof captured by one peer is rejected by another; the nonce cache and ±60 s
window stop reuse against the intended target (§6).

Can: drop, delay and reorder. Every capability degrades or stalls under this;
none fails open.

**The window where this attacker matters is pairing.** §5 states plainly that
pairing runs on a connection where neither side has a pinned key yet, so TLS
provides confidentiality but no authentication. The QR offer authenticates A to
B (B MUST verify A's TLS key against `identity_fingerprint` before proceeding,
§5.1); the six-digit SAS authenticates B to A (§5.2). A MITM would need to
produce two different key exchanges, and its two SAS values then differ. The
entire security of pairing is the user actually comparing six digits on two
screens, which is why §5.2 forbids implementations from offering a
"skip verification" path. Six digits is 10^6 — a single online attempt by an
active attacker succeeds with probability 10^-6, which is the accepted figure.

### 4.3 Hostile device on the same LAN

Can send mDNS announces and unicast announces claiming any DeviceID (§15.1,
§15.2). Achieves nothing beyond a failed handshake: discovery emits candidates
and never dials, and a discovered peer is still authenticated by pinned key
(§15.2, HLD §3.2). Can therefore waste a peer's dial attempts, which is a DoS,
not a compromise.

**Learns more than it can do.** mDNS TXT records broadcast DeviceID and display
name in the clear to the whole segment (§15.1). Any café, campus or hotel network
a device joins learns a stable identifier for it. §7.4 covers what that composes
into.

An unpaired hostile device cannot initiate anything: there is no message in this
protocol addressed to anyone but a paired device of the same user (Appendix C).

### 4.4 Malicious or compromised rendezvous operator

Learns, in §16's own words: which DeviceIDs exist, their IP endpoints, when they
are online, and who looks up whom — a social graph of the user's own devices.

Can: refuse to answer (denial of service — the relay path also flows through
rendezvous signalling for hole punching, §18); return stale-but-validly-signed
registrations within their ≤10 minute lifetime, steering a peer at dead or
attacker-chosen endpoints; correlate all of the above over time.

Cannot: forge a registration. The server MUST verify the Ed25519 signature by
the identity key before storing, so only the key holder can move a device's
endpoints (§16). Cannot impersonate a device, because steering a dial somewhere
else still ends at B1 — the pinned-key check — and fails. Cannot read content;
it carries none.

Note that `LookupRequest` (§16) carries no authentication. Anyone who knows a
DeviceID can ask the rendezvous where that device currently is. See §7.4.

Mitigation is self-hosting, which §16 states is why the design keeps rendezvous
trivially self-hostable and never a required third party (PRD R7).

### 4.5 Malicious or compromised relay operator

Learns, in §17's own words: which DeviceIDs talk to each other, when, and how
much. Not content.

Can: drop or delay frames (DoS, and a forced degradation — path racing falls back
to relay when direct dies, §18, so a relay that misbehaves selectively can hold a
session on a path it controls); observe volume and timing, which for `mirror` and
`input` is a fairly rich side channel; deliver frames to a mailbox it should not
(mitigated: MUST NOT deliver to a DeviceID that has not authenticated, §17).

Cannot: read or modify payloads. The payload is a complete QUIC packet, so
end-to-end encryption is unchanged when a path is relayed — the relay is a
network element, not a participant (§17, PRD R27). Modification breaks QUIC's
own AEAD and is indistinguishable from corruption. Holds no keys.

The relay does authenticate its clients (`RelayHello`/`RelayChallenge`/
`RelayAuth`, §17). That mechanism has a defect — see §7.5.

### 4.6 Local unprivileged process, same machine, same user

This is the adversary the design is weakest against, and the weakness is
structural rather than accidental.

Can: open the IPC socket, because the socket's access control is filesystem
permissions and a named-pipe ACL (D-29) and those are per-user. It then drives
the daemon with the full message set (§3, D-29) — push clipboard, offer files,
enumerate paired devices, request an Owned session. Can read the `remotefs`
cache and the session log through whatever protection the filesystem gives them.
Can read a tier-2 encrypted privilege key file and grind it offline (D-21). Can
read the identity key (§7.2).

Cannot, on tier 1: extract the privilege key, which never leaves the keystore
where the keystore can sign in place (D-19's preferred refinement). Cannot mint
`AuthProof`s while the privilege key is sealed.

Partially mitigated by D-30: because unlock is scoped per peer, the prompt names
the target — "Unlock to access `desktop-home`". A local process that asks the
daemon to start an Owned session produces a prompt the user can recognise as
unexpected, rather than a contentless "Unlock OpenAir" that trains reflexive
approval. That is a real defence and it is a side effect of a UX argument, not of
a security one.

**D-21 states the general principle and it is the right one: against a
live-compromised machine nothing at this layer helps, because an attacker with
filesystem access can equally keylog the credential the next time it is typed.
No tier closes the warm threat, and pretending otherwise would be the more
dangerous error.**

### 4.7 Physical access to an unlocked device

Equivalent to §4.6 with a human at the keyboard, plus whatever the OS session
already grants. If a six-hour unlock token is live (D-18), Owned access to the
peer that token was scoped to (D-30) is available immediately, with no further
challenge — the challenge gates *starting* a session, not each operation within
it, which is deliberate and is what preserves S3.

D-18 is explicit about what is and is not being claimed here, and the claim is
worth repeating verbatim in a threat model: the gate "raises the bar against
casual theft — a stolen unlocked laptop, someone sitting down at your desk — but
not against an attacker who can read the filesystem."

The visible-indicator and session-log requirements (§6.3, PRD R4) do not help
against this adversary at all: they are on the *accessed* device, and this
adversary is at the initiating one.

### 4.8 Physical access to a powered-off device

This is the threat tier 1 exists for, and D-21 is precise about it: "a PIN's
offline weakness bites only under the *cold* threat — a stolen powered-off disk,
a leaked backup, a home directory synced to cloud storage."

- **Tier 1 (keystore/TPM), six-hour default policy.** The privilege key is
  sealed and the hardware enforces attempt limits, so offline brute force is not
  available at all (D-21). The identity key is still readable — see §7.2 — so the
  attacker inherits Trusted-level impersonation but not Owned.
- **Tier 1, `auth_policy = never` (always-on).** Not equivalent. See §7.3; this
  is the one place where D-21's "tier 1 closes the cold threat completely" does
  not hold.
- **Tier 2 (passphrase).** Four diceware-style words is roughly 51 bits, which at
  about one second per attempt is computationally out of reach offline (D-21).
  This is why D-21 requires a passphrase and rejects a numeric PIN: 10^4–10^6 of
  entropy grinds in days single-threaded and hours parallelised, and Argon2id
  parameters tuned to be tolerable on a phone are affordable on a workstation GPU
  (D-19 item 2, D-21).
- **Tier 3.** No protection whatsoever. The device holds no privilege key at all,
  which is the point: it cannot initiate Owned operations and cannot be
  designated always-on (D-21).

---

## 5. What the infrastructure operators actually learn

Stated together because the two are usually conflated, and because "end-to-end
encrypted" is routinely read as "the operator learns nothing", which is false.

| | Rendezvous (§16) | Relay (§17) |
|---|---|---|
| Content | none — carries none | none — opaque QUIC packets, MUST NOT inspect or modify |
| Device identifiers | every registered DeviceID | both DeviceIDs on every frame |
| Network location | IP endpoints, reflexive and local, per registration | source IP of each connected client |
| Time | online/offline transitions at ≤10-minute heartbeat granularity | exact timing of every frame |
| Volume | none | byte counts per pair, per direction, continuously |
| Relationships | who looks up whom | who talks to whom |

A rendezvous operator reconstructs the user's device inventory, their home and
travel IP addresses over time, and their daily pattern of which machines are on.
A relay operator additionally reconstructs *activity*: a 2 GB burst is a file
transfer, a sustained few-Mb/s stream is a mirror session, a trickle of small
datagrams is someone typing. Neither ever sees a byte of user content, and
neither can impersonate a device, because both sit outside boundary B1.

Both sections give the same answer to this: self-host. PRD R7 requires the
rendezvous never be required to be a third party's, PRD NG5 explicitly declines
to run a hosted relay for the public, and both components are one binary each and
can run in one process for small deployments (HLD §2).

---

## 6. Weaknesses accepted rather than solved

These matter more than the parts that work. Each is a deliberate choice recorded
in a decision entry, not an oversight.

### 6.1 Per-peer unlock scope is policy, not cryptography

D-30 scopes one unlock to one peer for six hours. D-30 also states the limitation
in its own words: unlock decrypts the privilege key into RAM for six hours, and a
key sitting in memory can sign for *any* peer, so per-peer scope is enforced by
policy in our own daemon, not by cryptography. It bounds what a well-behaved
implementation does and makes the prompt meaningful; **it does not stop a daemon
compromised mid-session from signing for peers the user never unlocked.**

D-30 records the refinement that would close this — ephemeral per-peer delegation
keys, signed by the privilege key at unlock, privilege key immediately re-sealed,
per-request `AuthProof`s made by the ephemeral key and verified against the
delegation, SSH-certificate shaped. It is explicitly **not adopted**, and is
flagged for the maintainer. Until it is, the honest statement of the property is:
*per-peer scope is a UX guarantee and a defence against our own bugs; it is not a
defence against code execution in the daemon.*

### 6.2 `SessionKill` is a courtesy message

§6.3 and D-25 are explicit: `SessionKill` tells a well-behaved peer why a session
ended. The enforcement is the accessed device refusing further operations and
resetting the relevant streams. This is the right way round — specifying it the
other way would have made PRD R4's guarantee depend on the goodwill of the party
being revoked (D-25) — and it means a misbehaving peer cannot ignore its way out
of a kill.

What local enforcement does **not** recover:

- Data already delivered. A kill stops the next byte, not the last hour.
- Owned operations already in flight elsewhere, if the killed peer holds a
  session with a *third* device. Kills and revocations are per-pair.
- The privilege key of the killed peer, which is still valid until the revoking
  device discards its pin (§6.1, `new_level = UNPAIRED`).

And one thing that is genuinely unspecified rather than merely bounded: §6.1
requires the peer to stop honouring operations above `new_level` and to abort
in-flight ones that exceed it, but says nothing about closing the QUIC
connection. See §7.6.

### 6.3 A tier-3 device protects its privilege key not at all

D-21 tier 3 is "neither available" — no keystore, no TPM, no passphrase path
taken. Such a device holds no privilege key, may pair and operate at Trusted
level, and MUST NOT be granted Owned; §7.3 of `PROTOCOL.md` requires
implementations to surface this rather than degrading silently, and D-21 requires
the UI to state it plainly, on the reasoning that a user who believes they have
unattended access and does not is worse off than one who was told.

The gate depends on the peer honestly reporting `protection_tier` in `Hello` and
in the pairing messages (§4, §5.2, §7.3). There is no attestation. See §7.7.

### 6.4 A stolen device is a Trusted-level impersonator immediately

D-20 states this consequence up front, "because it nearly went the other way": a
stolen device whose identity key is warm can still impersonate the owner at
Trusted level — offer files, push clipboard — with consent required on the far
end. That blast radius is bounded but real.

It follows directly from the design and cannot be fixed within it: the identity
key is warm *by construction*, because gating it would make the device
unreachable while nobody is present, which is the entire scenario PRD G5 and S3
exist to serve (D-20). The continuity features ride the same warm key, so
notification mirroring (R21) and clipboard sync (R19) keep working while the
privilege key is locked — D-20 chose this deliberately, on the grounds that a
phone that silently stopped mirroring notifications after six hours idle would
get the feature blamed rather than the policy.

The mitigation available to the user is revocation (§6.1, PRD R5), which requires
noticing the theft.

### 6.5 The unlock timer is absolute, and that is the only thing bounding a live compromise

D-18 rejected a sliding window explicitly: a sliding window means an attacker who
keeps the session active is never locked out, which inverts the purpose. Expiry
is absolute from grant. The one-hour grace for in-flight work (§6.5, D-25) is
capped for the same reason — without a cap, starting a long operation just before
expiry would extend access indefinitely.

Six hours is nonetheless six hours, and `auth_policy = never` is unbounded. D-18
requires "never" to be as deliberate and as visible as promotion to Owned, and to
appear in the paired-device list rather than hiding in settings.

### 6.6 Key rotation is not possible without re-pairing

Pinned keys mean a rotated key hard-fails by design (§2, D-7, Appendix C). There
is no revocation-and-reissue ceremony. A device that suspects key compromise
re-pairs everything; a forgotten tier-2 passphrase is unrecoverable and means
re-pairing every device (D-19). Appendix C names this as deliberate and notes a
rotation ceremony could be added later — it is not needed for any Phase 1–4
capability.

### 6.7 The daemon holds a decrypted Ed25519 key in Go memory

Where the platform cannot sign inside the keystore, D-19's fallback decrypts the
privilege key into RAM for the token lifetime, and D-19 is blunt that "hold
decrypted state for 6 hours is the entire security boundary, so this is not a
detail". The requirements it creates — lock the pages (`mlock`/`VirtualLock`),
disable core dumps for the daemon, zero the buffer on expiry, manual end and
shutdown — are implementation obligations that this threat model depends on and
that are not yet built. Until they are, swap and core dumps are live exposure
paths for the privilege key on the fallback path.

---

## 7. Open questions and defects found while assembling this

These are not accepted weaknesses — they are places where the existing decisions
do not answer the question, or answer it inconsistently. They need a maintainer
call, and several imply `PROTOCOL.md` changes. **Nothing here has been changed in
any other document.**

### 7.1 The trust store has no specified integrity or confidentiality at rest

D-30 gives the authoritative field list — pinned identity and privilege public
keys, `level`, `granted_capabilities`, `auth_policy`, `protection_tier`. Nothing
in `PROTOCOL.md` or in any decision entry specifies how that record is stored or
protected. Appendix A covers the privilege key only.

Consequence: an attacker with **write** access to the trust store file inserts
their own public keys as an Owned peer, or flips an existing peer's `level` to
owned, or rewrites a `protection_tier`, and every check in §6 and §7.3 then
passes honestly. This is a cheaper path to full Owned access than attacking any
of the cryptography above it, and it is available to the §4.6 local-process
adversary and to the §4.8 cold adversary alike.

The natural fix is to authenticate the trust store under a key the same hardware
protects, but that is a design decision and it belongs to the maintainer. Flagged
as an open gap, deliberately not papered over.

### 7.2 The identity key's at-rest protection is unspecified

§2 says the identity key is "never" gated and Appendix A covers only the
privilege key. So the identity key is presumably on disk in the clear, but no
document says so, and D-20's "warm" means "usable without user presence" rather
than "unencrypted" — a TPM-sealed, PCR-bound, no-presence-required key would also
satisfy D-20 and would be materially stronger against §4.8.

This matters because §6.4's blast radius is exactly the identity key's. Whether
cold theft yields Trusted-level impersonation, or yields nothing until the
machine boots into its measured state, currently depends on an implementation
choice nobody has recorded.

### 7.3 D-20 and D-21 conflict on the always-on device under cold theft

D-21 claims: "Tier 1 closes the cold threat completely."

D-20 specifies: a device set to `auth_policy = never` "keeps its privilege key
unsealed continuously, sealed to boot state in the TPM so it auto-unseals with no
human present."

A TPM policy bound to boot state and nothing else releases the key to whoever
boots the machine into that state — which an attacker holding the stolen hardware
can do, because the measured state is a property of the machine, not of the user.
For the always-on desktop, which is precisely the device holding the most
valuable Owned access, cold theft therefore yields the privilege key. This is the
same shape as TPM-sealed disk encryption without a PIN.

D-21 anticipates part of this — its consequences note that "Linux TPM work is two
policies, not one: sealing to PCRs for the always-on case ... and sealing with
user presence required for the interactive case" — but it does not carry the
implication back into its own claim about the cold threat. The two entries are
individually defensible and jointly inconsistent. **Open: what, if anything,
protects an always-on device's privilege key against theft of the powered-off
hardware?** Physical access to an always-on machine being "largely game over
regardless" (D-19) is an argument about the *running* machine and does not
transfer to the powered-off one.

### 7.4 DeviceID is a permanent tracking identifier, and rendezvous lookup is unauthenticated

Two facts that are individually noted and never composed:

1. mDNS TXT records broadcast DeviceID and display name in the clear on every LAN
   the device joins (§15.1).
2. `LookupRequest { device_id }` (§16) has no authentication, and the response
   carries current IP endpoints.

So a hostile device on any network the user's laptop has ever joined — café,
campus, conference — learns a stable DeviceID, and can thereafter query the
rendezvous to learn that device's current IP endpoints indefinitely. DeviceID is
derived from the identity key (§2) and, per §6.6, never rotates.

§15.2's reassurance that "a hostile announce achieves nothing beyond a failed
handshake" addresses the *inbound* direction only. The disclosure direction is
not addressed anywhere. Whether lookups should be authorised (only paired peers
can resolve a DeviceID) is an open design question with real cost — it needs a
per-peer capability or blinded identifier scheme — and is not decided.

### 7.5 `RelayAuth` lacks domain separation and does not bind the relay's identity

§17: `RelayAuth { signature }` is an Ed25519 signature "over both nonces" — the
client's `RelayHello.nonce` and the server's `RelayChallenge.server_nonce`.

Every other signed structure in the protocol has a domain-separation prefix and
binds its context: `"openair-pair-v1"` plus both key pairs (§5.2),
`"openair-owned-v1"` plus the target DeviceID, capID and msgType (§6),
`"openair-rendezvous-v1"` plus the endpoints (§16). §17 has neither a prefix nor
the relay's identity.

Consequence: a hostile relay A can act as a proxy — take its own challenge from
relay B, present B's `server_nonce` to a connecting client, collect the
signature, and authenticate to B as that client. It then owns the client's
mailbox on B and can receive frames addressed to it there, or deny them. Bounded
(no plaintext, and B1 still blocks impersonation to peers) but real, and it is a
defect in `PROTOCOL.md` §17 rather than an accepted tradeoff. Reported, not
fixed, per this track's scope.

### 7.6 §6.1 does not require closing the connection on unpair

`Revoke { new_level = UNPAIRED }` requires discarding both pinned keys and
refusing operations above the new level (§6.1). Pinning gates *new* handshakes.
An already-established QUIC connection was authenticated before the pins were
discarded and remains cryptographically valid; nothing in §6.1 says it MUST be
closed. A revoked peer that keeps the connection open is then relying on the
revoker's per-message level checks alone, which is a strictly larger surface than
"the connection is gone". PRD R5 requires revocation to take effect immediately,
including mid-session, so the intent is clear; the MUST is missing.

### 7.7 `protection_tier` is self-reported and unattested

`Hello.protection_tier` and the pairing messages carry the peer's own claim about
how it protects its privilege key (§4, §5.2, §7.3, D-21). A device that lies
claims tier 1 while storing its privilege key in the clear, and the §7.3 rule
that tier 3 MUST NOT be granted Owned then never fires.

This is arguably fine — the peer is the user's own device, and a compromised peer
has better attacks available — but it means the tier gate protects a *cooperating*
user from misconfiguring their own fleet, and does not protect anyone from a
lying peer. D-21 requires the tier to be "recorded in the trust store *and*
visible to peers, so a device deciding whether to grant Owned access can see
whether the requesting device actually protects its privilege key"; "actually"
overstates what a self-reported field can deliver. Worth stating precisely rather
than resolving.

### 7.8 The `remotefs` cache MUST be encrypted, under an unspecified key

§11.4 requires the LRU disk cache to be encrypted at rest and size-capped,
"because it holds someone else's files (PRD K8)". No key is specified. If it is
encrypted under a warm, ungated key, it is protected against a different-user
process and not against §4.8. If under the privilege key, the cache is unreadable
whenever the token has expired, which likely breaks the read-ahead behaviour
§11.4 describes. Neither is decided. PRD K8 lists cache privacy as an open risk,
so this is consistent with the PRD — but it is not answered.

### 7.9 §5.1's "short code for manual entry" is under-specified

PRD R2 offers "QR scan or short PIN comparison". §5.1 says A displays "a QR code,
or a short code for manual entry, containing" a `PairOffer` — which includes a
16-byte identity fingerprint, LAN hints and a protocol version. That is not a
short code, and it is not humanly typeable. If the manual path instead carries a
genuinely short value, then B's verification of A (§5.1) drops from a full
fingerprint comparison to a low-entropy one, and the security argument in §5.2
changes. As written the two paths are stated as equivalent and are not.

### 7.10 D-18 and D-19/Appendix A disagree on how the passphrase is verified

D-18 requires the Linux credential to be "stored as a KDF hash (argon2id) and
never alongside the sealed key, with rate limiting on attempts". D-19 and
Appendix A instead derive `K_master` from the passphrase via Argon2id and verify
correctness implicitly through the AEAD tag — no separate verifier is stored.

Appendix A is the later and better design: storing a separate hash would add a
second offline oracle beside the one Appendix A already acknowledges, for no
gain. But D-18's text was never superseded on this point, and an implementer
reading the decision log in order would build the weaker thing. Flagged for a
superseding note; not edited here.

---

## 8. Non-goals

Explicitly out of scope, so their absence reads as a decision rather than an
oversight. Appendix C of `PROTOCOL.md` is the authoritative list of what the
*protocol* deliberately does not specify; this section states the security
consequences.

- **Defending one user against another on the same machine.** OpenAir has one
  principal. The IPC socket's access control is per-user (D-29, §3 above), and
  D-21 states that against a live-compromised machine no tier helps. A malicious
  process running as the user is outside the model.
- **Multi-user, group, or third-party sharing.** PRD NG2 and Appendix C. There is
  no message in this protocol addressed to anyone but a paired device of the same
  user, so there is no access-control model beyond the two-level ladder in §6.
- **Multi-device coordination.** No consensus, no replication (D-11, Appendix C).
  Every capability is strictly pairwise, so there is no distributed-state attack
  surface and equally no way to make revocation propagate across a fleet — each
  pair is revoked independently.
- **Traffic-analysis resistance.** No padding, no cover traffic, no timing
  defences are specified anywhere. §4.1 and §5 describe what that costs.
- **Hiding metadata from infrastructure operators.** Explicitly accepted; the
  answer is self-hosting (§16, §17, PRD R7, PRD NG5).
- **Key rotation without re-pairing.** Appendix C, §6.6 above.
- **Interop with AirDrop/Quick Share.** PRD NG1 — and therefore no exposure to
  their wire formats or their vulnerabilities.
- **Anti-forensics on the accessed device.** §6.3 requires a local session log by
  design (PRD R4). It is a feature, and it is also a record of activity that
  anyone with local access can read.
- **Protecting against a malicious *paired* device.** Pairing is an assertion
  that the device is yours. A paired device at Owned level is, by definition,
  authorised to do everything Owned permits; there is no sandboxing of a peer's
  requests beyond the level and capability checks in §6.

---

## 9. Coverage against PRD R29

R29 asks for a threat model "including the unattended-access model: what a stolen
paired laptop can do, what revocation guarantees, what a malicious
relay/rendezvous operator can and cannot learn."

- *What a stolen paired laptop can do* — §4.7 (unlocked), §4.8 (powered off),
  §6.4 (the warm identity key's blast radius), §6.1 (the policy-not-cryptography
  limit on unlock scope). Open: §7.2, §7.3.
- *What revocation guarantees* — §6.2, with the three things local enforcement
  does not recover. Open: §7.6.
- *What a malicious relay/rendezvous operator can and cannot learn* — §4.4, §4.5,
  and the table in §5. Open: §7.4, §7.5.

R29 is satisfied for the documentation requirement. It is **not** a statement
that the system is secure against everything listed: §7 contains ten items, of
which §7.1 (unauthenticated trust store), §7.3 (always-on cold theft) and §7.5
(`RelayAuth`) are the three that would change a security property if answered
differently.

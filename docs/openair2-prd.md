# OpenAir 2.0 — Product Requirements Document

**Author:** Shreyash
**Status:** Draft v2
**Date:** July 2026

---

## 1. Overview

OpenAir is an open-source, cross-platform device-continuity tool. It makes all your devices behave like one system: send files at wire speed, copy on one machine and paste on another, see your phone's notifications on your desktop, browse and stream files sitting on another device without downloading them, mirror screens, and control machines remotely — regardless of operating system, on the same network or across the internet.

v1.0 proved the transfer core (parallel-TCP LAN file transfer, 148 Mb/s on 5GHz WiFi, 477 Mb/s on direct hotspot) but was architected only for file transfer. v2.0 is a ground-up rebuild on a transport and connection architecture that carries the full vision.

## 2. Problem statement

Device continuity today is locked inside vendor ecosystems. AirDrop, Universal Control, Handoff and iPhone Mirroring only work Apple-to-Apple. Quick Share and Phone Link are Android/Windows-centric, partially closed, and don't cover Linux at all. Cross-ecosystem users — an Android phone, a Linux dev machine, a Windows laptop — have no first-class way to get the full continuity experience without stitching together four tools (LocalSend + KDE Connect + a VPN + scrcpy), each with its own pairing, discovery and gaps.

**Target user (initial):** technical, multi-device individuals — developers and power users who own devices across ecosystems and want their own devices to behave like one system, at home and away from home. Not teams, not enterprises, not casual one-off sharing to strangers.

## 3. Product vision

One daemon on every device you own. Devices pair once — after that they find each other automatically and stay reachable everywhere: same WiFi, different networks, behind NATs, phone on mobile data. Every capability rides the same secure connection and works identically whether the path is LAN-direct, hole-punched or relayed. Your machines are usable even when you're not sitting at them. Open protocol, open source, no account required, no cloud in the data path.

## 4. Goals and non-goals

### Goals
- G1: **Windows, Linux and Android are all first-class citizens** — every capability ships on all three, with parity as the bar. macOS is supported best-effort where its APIs allow.
- G2: File transfer that saturates the physical link (benchmark gate: within 15% of v1.0's numbers on identical hardware).
- G3: Clipboard continuity between paired devices; manual clipboard push everywhere.
- G4: **Location-independence: every capability works over NAT/cross-network, all the time.** LAN is a fast path, never a requirement. NAT traversal with relay fallback guarantees connectivity; direct P2P whenever possible.
- G5: **Unattended access.** Pair once (TOFU), then reach, use and control your machines while physically away from them — no human on the remote end needed.
- G6: Notification mirroring from peer devices (phone → desktop first).
- G7: Remote file access: browse another device's files and open/stream them on demand — including seeking through large media — without fully downloading first.
- G8: Screen mirroring and remote input control at usable latency (sub-50 ms glass-to-glass on LAN target; best-effort over WAN with graceful degradation).
- G9: End-to-end encryption everywhere, including through relays. Keys pinned at pairing.
- G10: A written, versioned, open protocol spec — the spec is a deliverable, not an afterthought.

### Non-goals (v2.0)
- NG1: Interop with AirDrop/Quick Share wire protocols (reverse-engineered, fragile, not portable).
- NG2: Multi-user sharing, contacts, or sending to strangers. This is *your devices*, not a social sharing layer.
- NG3: iOS support (background and screen-capture restrictions make it a separate investigation; revisit post-Phase 2).
- NG4: File *sync*/reconciliation (Syncthing's job). OpenAir transfers and streams; it does not merge folders.
- NG5: Hosted/managed relay service for the public. Users self-host or use community relays.

## 5. Personas and key scenarios

**P1 — The cross-ecosystem developer (primary; the author's own use case).** Linux desktop at home, Windows laptop, Android phone.
- S1: Copies a snippet on the desktop, pastes it on the laptop within 1 second, nothing clicked.
- S2: Drops a 2 GB build artifact from desktop to laptop over LAN in under a minute.
- S3: **From a hostel/campus network, connects to the home desktop (behind CGNAT) that nobody is sitting at:** browses its filesystem, streams a lecture recording stored on it with instant seek, grabs a repo, then opens a mirrored screen session and drives it with keyboard/mouse.
- S4: Phone's notifications (messages, OTPs) appear on the desktop while he's working; he dismisses them from the desktop.
- S5: Phone on mobile data shares a photo to the home desktop; it arrives exactly as it would on LAN, just slower.

## 6. Product requirements

### 6.1 Pairing, identity & unattended access
- R1: Each device has a long-term keypair; device identity = key fingerprint. No accounts, no server-side identity.
- R2: **Pairing is trust-on-first-use and happens once**, via QR scan or short PIN comparison, in under 30 seconds. Peer keys are pinned; a changed key hard-fails with a re-pair prompt.
- R3: **Trust levels per paired device.** "Owned" devices get unattended access: connect, browse, transfer, mirror and control with no per-session approval on the remote end. "Trusted" devices require per-session or per-capability consent. Default for new pairings is Trusted; promotion to Owned is an explicit, deliberate action on the target machine.
- R4: A device being remotely accessed shows a visible indicator when mirrored/controlled, and keeps a local session log. A local user can always kill a session instantly.
- R5: Paired-device list is manageable: rename, demote, revoke (revocation takes effect immediately, including mid-session).

### 6.2 Discovery & connectivity
- R6: Paired devices on the same LAN discover each other within 3 seconds of both being online (mDNS + unicast fallback).
- R7: Across networks, devices locate each other via a rendezvous server keyed by public key. Self-hostable; never required to be a third party's.
- R8: **Capability parity across paths:** every feature in this document works over LAN-direct, hole-punched and relayed connections. Features may degrade in quality (bitrate, latency) over constrained paths but never in availability.
- R9: Connections race paths and upgrade live (relay → direct) without dropping sessions; sessions survive network changes (WiFi → mobile data) without re-pairing or restarting transfers.
- R10: Devices remain reachable while idle: desktop daemons always-on; Android maintains reachability via a persistent foreground service (visible ongoing notification is the accepted UX cost of first-class status).

### 6.3 File transfer
- R11: Send files/folders to any paired device; receiver accepts/declines (auto-accept for Owned devices).
- R12: Throughput within 15% of the physical link's practical maximum; explicit benchmark vs v1.0 baseline.
- R13: Transfers resumable after interruption; integrity verified per chunk; progress/speed/ETA visible; cancellable from either side.

### 6.4 Remote file access & streaming (no-download use)
- R14: Browse the filesystem of a paired device (Owned, or Trusted with consent): directory listing, metadata, thumbnails for media.
- R15: **On-demand reads:** open a remote file and fetch only the byte ranges actually consumed — enabling playing a video with instant seek, previewing a document, or reading part of a large file without transferring the whole thing.
- R16: Remote media streaming supports seek within 1 s on LAN / 3 s relayed, for files at least up to tens of GB.
- R17: Optional transcode-on-source for formats the consuming device can't play natively (stretch; raw range-streaming is the baseline).

### 6.5 Clipboard
- R18: Manual "push clipboard to device" on all platforms.
- R19: Opt-in automatic clipboard sync (text first, images later); best-effort where OS policy restricts background clipboard reads, with the manual path as the guaranteed baseline.
- R20: Clipboard content never persisted server-side or relayed in plaintext; auto-sync off by default.

### 6.6 Notifications
- R21: Notification mirroring from phone to desktops: app name, title, body, icon; dismiss-on-one-dismisses-everywhere.
- R22: Per-app filtering on the source device (sensitive apps excludable); actionable notifications (inline reply) as a fast-follow.
- R23: Desktop → desktop notification forwarding (e.g. long build finished on the home machine) via the same channel.

### 6.7 Screen mirroring & remote input
- R24: Live screen mirroring of a paired desktop or Android device using hardware encoding; target sub-50 ms glass-to-glass on LAN, adaptive bitrate/resolution over WAN — degrade, never stall.
- R25: Keyboard/mouse (and touch, for Android targets) control with drop-stale semantics; input never queues behind bulk transfers on the same connection.
- R26: Mirroring and control of Owned devices requires no remote-end human (subject to OS constraints — e.g. Android's screen-capture consent model; see K6).

### 6.8 Security & privacy (cross-cutting)
- R27: All traffic end-to-end encrypted; relays and rendezvous see only ciphertext and routing metadata.
- R28: No telemetry by default; diagnostics opt-in and documented.
- R29: Threat model documented in the spec, including the unattended-access model: what a stolen paired laptop can do, what revocation guarantees, what a malicious relay/rendezvous operator can and cannot learn.

### 6.9 Non-functional
- R30: Daemon idle footprint fit for always-on (<50 MB RSS desktop, negligible idle CPU; Android foreground service within Doze/battery norms).
- R31: Single static binary per desktop platform; standard APK for Android; no runtime dependencies.
- R32: Protocol spec versioned from message one; capability negotiation so mixed-version device fleets coexist.

## 7. Success metrics
- M1: Benchmark gate (R12) passed and published with methodology.
- M2: Author's full device set (Linux desktop, Windows laptop, Android phone) runs OpenAir daily for 30 consecutive days without falling back to LocalSend/KDE Connect/scrcpy (dogfooding metric).
- M3: Clipboard copy→paste <1 s LAN, <2 s relayed. Notification mirror <2 s.
- M4: Remote video file: playback start <2 s and seek <3 s over a relayed connection.
- M5: Cross-network: 100% connectivity (relay guarantees it), ≥60% of sessions upgrading to direct P2P under real Indian network conditions (CGNAT-heavy; measure on Jio/Airtel and publish).
- M6: One full remote work session (S3) completed from a network away from home, using only OpenAir.
- M7: External signal: first outside contributor or 100 GitHub stars.

## 8. Phased delivery

| Phase | Scope | Exit criteria |
|---|---|---|
| 1 | Pairing/TOFU + trust levels, LAN discovery, file transfer, manual clipboard (Linux + Windows + Android from the start) | R1–R6, R11–R13, R18; benchmark gate M1 |
| 2 | Cross-network everything: rendezvous, hole punching, relay, live path migration, Android persistent reachability | R7–R10; M5 measured |
| 3 | Continuity layer: notifications, auto clipboard, remote file browse + range streaming | R14–R23; M3, M4 |
| 4 | Screen mirroring + remote input (desktop and Android targets) | R24–R26; latency measured; M6 |

Note: NAT traversal moves to Phase 2 deliberately — everything built after it inherits "works over NAT" for free, which is what makes R8 enforceable rather than aspirational.

## 9. Risks & open questions
- K1: Userspace QUIC may miss the throughput gate off-Linux (no GSO on Windows). Mitigation: parallel-stream bulk mode reusing the v1.0 engine.
- K2: CGNAT on Indian carriers may push direct-P2P below published figures; relay bandwidth becomes a real cost for streaming-heavy use. Mitigation: self-hosted relay guidance, adaptive bitrate, measure early.
- K3: OS clipboard restrictions (Android 10+, Wayland variance) make auto-sync best-effort → manual push is the guaranteed baseline.
- K4: Sub-50 ms mirroring over QUIC datagrams is unproven; Moonlight-style raw UDP media path is the designed fallback.
- K5: Android first-class status depends on a persistent foreground service surviving OEM battery killers (Xiaomi/Samsung aggressive Doze). Needs testing on real Indian-market devices, not just Pixels.
- K6: Fully unattended Android *mirroring* may be impossible by policy — MediaProjection requires a user consent tap per session on-device. Unattended mirroring of desktops is fine; for Android targets this may cap at "consent once per boot" or require ADB-granted permissions. Research needed.
- K7: Notification access on Android requires NotificationListenerService permission (fine), but *dismiss sync* and inline reply have OEM quirks.
- K8: Range-streaming remote files with instant seek over a relayed path is bandwidth-sensitive; may need read-ahead heuristics and local caching strategy (cache eviction, privacy of cached content).
- K9: Crypto layering decision (QUIC's TLS 1.3 with pinned certs vs Noise_IK) needs a one-page decision doc before transport work.
- K10: Unattended access raises the stakes of key theft: an attacker with a paired device's key owns your machines. Open question: require a second factor (device-local biometric/PIN) to *use* Owned-level access, or accept SSH-like semantics?

## 10. Out-of-scope appendix: why not just use X
- **LocalSend:** LAN file transfer only; no NAT story, no continuity features.
- **KDE Connect:** closest in spirit (notifications, clipboard, some remote input) but LAN-centric, no NAT traversal, no streaming/mirroring, no remote file range-streaming, and desktop Linux-first in practice.
- **Syncthing:** folder sync, not ad-hoc transfer, streaming or continuity.
- **scrcpy:** excellent Android mirroring, but one-directional, USB/ADB-centric, no ecosystem.
- **Tailscale + tools on top:** solves connectivity, not continuity; closed control plane. OpenAir aims to be the integrated open equivalent for personal device continuity.

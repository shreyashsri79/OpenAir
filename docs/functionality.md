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

## Removed modules
`openair-cli`, `openair-receiver`, `openair-sender` deleted — see `docs/decisions.md` D-2. Project scope is now gui + android only. `openair-gui/internal/sender` and `internal/receiver` are the sole Go implementation.

## Sharp edges / open questions (fill in as discovered)
- Chunk wire format (offset/size/data framing) and handshake message format are described at a high level in README but not yet pinned down file-by-file here — next agent touching the protocol should fill this in from `openair-gui/internal/sender` and `internal/receiver`.

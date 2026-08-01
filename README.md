# OpenAir

<p align="center">
  <img src="./logo.png" alt="OpenAir Logo" width="500" />
</p>

**Direct device-to-device transfer and remote access over QUIC. No cloud, no account, no server in the middle.**

Two devices exchange keys once, in person, and from then on they can move files, share a clipboard, forward notifications, browse each other's shared folders, stream media, type on each other, and watch each other's screens. Everything is end-to-end encrypted between the two devices themselves; nothing you send passes through infrastructure anyone else runs, unless you have deliberately configured a relay you host.

---

## Contents

- [Install](#install)
- [Quick start](#quick-start)
- [How trust works](#how-trust-works)
- [What you can do](#what-you-can-do)
  - [Send and receive files](#send-and-receive-files)
  - [Clipboard](#clipboard)
  - [Notifications](#notifications)
  - [Browse and fetch remote files](#browse-and-fetch-remote-files)
  - [Stream a remote file into a player](#stream-a-remote-file-into-a-player)
  - [Type and click on another machine](#type-and-click-on-another-machine)
  - [Watch another machine's screen](#watch-another-machines-screen)
- [Away from home: reaching devices across networks](#away-from-home-reaching-devices-across-networks)
- [Reference](#reference)
  - [`openair` commands](#openair-commands)
  - [`openaird` flags](#openaird-flags)
  - [Files, ports and paths](#files-ports-and-paths)
- [Platform notes](#platform-notes)
- [Troubleshooting](#troubleshooting)
- [Security model in brief](#security-model-in-brief)
- [Documentation](#documentation)
- [License](#license)

---

## Install

Go 1.25 or newer, then:

```bash
git clone https://github.com/shreyashsri79/openair
cd openair
go build ./cmd/...
```

That produces four binaries:

| Binary | What it is |
| --- | --- |
| `openaird` | the daemon — one per device, always running |
| `openair` | the command-line client that drives it |
| `openair-rendezvous` | optional, self-hosted: helps your own devices find each other across networks |
| `openair-relay` | optional, self-hosted: carries traffic when no direct path exists |

Only the first two are needed on a LAN. Put them somewhere on your `PATH`.

Supported: Linux, Windows, macOS, Android. Windows is a hard build gate in CI, so it does not rot.

---

## Quick start

Do this once on each of two machines.

**1. Start the daemon.** It owns the device's identity, listens for peers, and keeps receiving files whether or not a terminal is open.

```bash
openaird
```

Leave it running (a systemd user unit, a login item, a `tmux` pane — whatever suits). Everything below assumes it is up.

**2. Pair the two devices.** On the first machine:

```bash
openair pair
```

It prints an offer — a `openair://pair/...` URL and the same thing hyphenated for typing — and waits. On the second machine, give it that offer:

```bash
openair pair openair://pair/AB3F...
```

Both screens now show **six digits**. Compare them with your eyes. If they match, confirm on both. If they do not, something is between you: say no.

There is deliberately no flag that skips those digits.

**3. Send a file.**

```bash
openair send holiday.mp4 laptop
```

`laptop` is the other device's name. A fingerprint prefix works too, and so does an explicit `10.0.0.5:9000` if you would rather not rely on discovery.

**4. Accept it on the other side.** Inbound transfers need a human unless you say otherwise:

```bash
openair watch
```

`watch` follows the daemon's events and prompts you to approve. For a headless box, start its daemon with `--accept-all` instead and paired devices can send without asking.

That is the whole basic loop. `openair status` says what the daemon is doing; `openair devices` lists what you have paired and what is visible on the network right now.

---

## How trust works

There are two levels, and the difference is worth understanding because it decides which commands work.

**Trusted** is what pairing gives you. Files, clipboard and notifications work at this level. Someone still has to approve an inbound transfer.

**Owned** is unattended access to a device: reading its files, driving its keyboard, watching its screen. It is not something pairing grants, and it takes three separate steps.

```bash
# once per device, before pairing anything you intend to own:
openair protect

# on the machine being reached, naming the machine that may reach it:
openair trust phone --owned

# on the machine doing the reaching, once every six hours:
openair unlock desktop
```

`protect` creates this device's privilege key and seals it with a passphrase — four words or more; it is refused if shorter, because an attacker who copies the sealed file can grind a short one offline. Run it **before** you pair, or re-pair afterwards: devices paired before `protect` never received the key.

The passphrase cannot be recovered. Forgetting it means re-pairing everything, by design.

`unlock` lasts six hours and covers exactly one device. `--for 30m` shortens it, `--never-expire` is for an always-on machine you have decided to treat that way, and `openair lock` ends a session immediately — or `openair lock` with no argument ends all of them. `openair trust laptop --trusted` takes Owned away permanently while keeping the pairing, and it also ends any live unlock rather than waiting for the timer.

---

## What you can do

### Send and receive files

```bash
openair send report.pdf notes.txt laptop     # several at once
openair send ./video.mkv 10.0.0.5:9000       # explicit address
```

Transfers resume: interrupt one, run it again, and only the chunks that were never verified are sent. Every chunk is checked against a digest before it is written, so corrupt bytes never reach the destination file. Received files land in `~/Downloads/OpenAir` unless the daemon was started with `--dir`.

You can also receive without a daemon at all, which is useful on a machine you are just borrowing:

```bash
openair recv --listen :9000 --dir ./inbox
```

### Clipboard

```bash
openair clip push laptop                # this machine's clipboard
openair clip push laptop "some text"    # or an argument
git log -1 | openair clip push laptop --stdin
```

For continuous sync, start the daemon with `--auto-clipboard`. It is off by default on purpose: a device that mirrors every copy also mirrors what your password manager put on the clipboard.

### Notifications

```bash
openair notify laptop --title "build finished" --body "12m 04s"
openair notify --title "build finished"          # every connected device
openair dismiss BUILD-123                        # clear it everywhere
```

With no device named, the notification goes to every device currently connected — the "my long build finished, tell me wherever I am" case. `openair watch` prints what arrives, including the key needed to clear it.

Filtering is done by the *sending* device, before anything is put on the wire: `--notify-allow` and `--notify-block` on `openaird` take app ids. Content you excluded never leaves the machine.

### Browse and fetch remote files

Needs Owned, and needs the far end to have shared something:

```bash
# on the machine holding the files:
openaird --share "$HOME/Music,$HOME/Documents"

# from the other machine, after `openair unlock desktop`:
openair ls desktop                       # the shares themselves
openair ls desktop Music/live-sets -l    # inside one
openair get desktop Music/set.flac
openair get desktop big.iso --out /mnt/big.iso
openair get desktop firmware.bin --offset 1048576 --length 4096
```

Shares are read-only and scoped to the directory you named. There is no write path, no per-file rule and no allow list inside a share: a directory you share is readable in full by any Owned device that has unlocked. Name them with `--share "music=$HOME/Music"` if two directories would otherwise have the same base name.

### Stream a remote file into a player

```bash
openair stream desktop Movies/film.mkv --with mpv
openair stream desktop Movies/film.mkv --open     # this desktop's default player
openair stream desktop Movies/film.mkv            # just print the URL
```

This does not copy the file. It publishes a loopback URL, and your player's ordinary `Range` requests become range reads against the remote machine — so a 40 GB file starts playing at once and seeking to the two-hour mark takes about one round trip rather than a download. Blocks are read ahead based on the bandwidth the connection is actually getting, and a seek cancels everything queued for the old position instead of waiting it out.

The URL is loopback-only and carries an unguessable token. It stays live while the daemon runs, and is dropped after fifteen idle minutes. `--stop` ends it early.

### Type and click on another machine

Needs Owned, and needs the target to have opted in with `openaird --accept-input`.

```bash
openair input laptop --text "hello from here"
openair input laptop --key enter
openair input laptop --key t --mods ctrl,shift
openair input laptop --move 40,0
openair input laptop --move-to 960,540
openair input laptop --click left
openair input laptop --scroll 0,-3
echo "long text" | openair input laptop --stdin
openair input laptop --stop        # ends the session, lowers the indicator
```

It is scriptable rather than interactive: there is no mode that grabs this machine's keyboard, because that needs a window to grab it in — and that window is the screen mirror below.

The device being controlled shows an indicator for as long as the session lasts, and anyone sitting at it can end the session locally. Anything held down is released after five seconds of silence, so a dropped connection cannot leave a key stuck on someone else's machine.

### Watch another machine's screen

Needs Owned, and needs the source to have opted in with `openaird --share-screen`, plus `ffmpeg` installed there.

```bash
openair mirror desktop --with mpv
openair mirror desktop --with 'mpv --profile=low-latency --untimed'
openair mirror desktop --fps 60 --bitrate 12Mb
openair mirror desktop --stop
```

Same idea as `stream`: a live URL that any player can open, H.264, rather than a video window this project would have to write. Each frame is sent on its own QUIC stream and a frame still being written when a newer one is ready is abandoned rather than finished — in realtime video a late frame is worse than a missing one, because it also delayed everything behind it. The bitrate adapts down when the path cannot carry what you asked for.

The machine being watched shows an indicator the whole time, and killing the session there stops the encoder rather than just hiding the picture.

---

## Away from home: reaching devices across networks

On one LAN, devices find each other by themselves and connect directly. Off it, you have two optional pieces, both of which you host:

**Rendezvous** lets your paired devices publish where they currently are, and answers STUN so a device behind NAT can learn its own public address:

```bash
openair-rendezvous --listen :9443
```

It prints the line to copy — `--rendezvous host:port@deviceid` — including the DeviceID your daemons pin. Nothing is persisted; entries expire within ten minutes.

**Relay** carries packets when no direct path can be made to work, which is the expected outcome behind symmetric NAT:

```bash
openair-relay --listen :9444
```

Then on each daemon:

```bash
openaird --rendezvous rv.example.com:9443@AB3F... --relay relay.example.com:9444@CD90...
```

With rendezvous configured, devices try to punch a direct path first and only fall back to the relay. A relayed session keeps trying to get off the relay for as long as it is up, and when it succeeds the transfer in flight is *not* restarted — the connection migrates underneath itself.

A relay operator sees encrypted packets and who is talking to whom. It cannot read anything, and it is not a participant in the protocol.

---

## Reference

### `openair` commands

```
openair status                                    what the daemon is doing
openair devices [--paired]                        paired devices, and what is visible now
openair watch [--yes]                             follow events, approve inbound transfers

openair pair                                      display an offer (daemon running)
openair pair --listen ADDR                        display an offer (no daemon)
openair pair OFFER [--addr ADDR]                  consume one that was scanned or typed
openair discover [--for 3s] [--watch]             list OpenAir devices on this network

openair send FILE... DEVICE|ADDR                  offer files to a device
openair recv [--listen :9000] [--dir DIR] [--yes] listen without a daemon
openair clip push DEVICE [TEXT] [--stdin]         push the clipboard

openair ls DEVICE [PATH] [-l] [--all]             list what a device shares
openair get DEVICE PATH [--out F] [--offset N] [--length N]
openair stream DEVICE PATH [--open|--with PLAYER] [--stop]
openair mirror DEVICE [--with PLAYER|--open] [--fps N] [--bitrate 8Mb] [--stop]
openair input DEVICE [--text T|--stdin] [--key K [--mods ctrl,alt]]
                     [--move X,Y] [--move-to X,Y] [--click BUTTON] [--scroll DX,DY] [--stop]

openair notify [DEVICE] --title T [--body B|--stdin] [--app ID] [--key K]
openair dismiss KEY [--device D] [--action ID [--reply TEXT]]

openair protect                                   create this device's privilege key
openair unlock DEVICE [--for 6h] [--never-expire]
openair lock [DEVICE]
openair trust DEVICE --owned|--trusted [--never-expire]
```

Common flags: `--socket PATH` for a non-default daemon socket, `--keys DIR` for a non-default key directory, and `--no-daemon` on `pair`, `send` and `clip` to drive a session from that process instead. `discover` and `recv` are always direct — `devices` is the daemon's own view of the same question, and `openaird` replaces `recv` entirely.

### `openaird` flags

| Flag | Meaning |
| --- | --- |
| `--listen :9000` | address to accept peers on |
| `--dir DIR` | where received files land (default `~/Downloads/OpenAir`) |
| `--name NAME` | what other devices call this one (default: hostname) |
| `--keys DIR`, `--socket PATH` | non-default locations |
| `--accept-all` | accept every transfer from a paired device without asking |
| `--no-announce` | do not advertise this device on the LAN |
| `--share DIRS` | directories Owned peers may browse, comma-separated, `name=/path` to disambiguate |
| `--share-screen` | allow screen mirroring (off by default) |
| `--mirror-command CMD` | what captures and encodes; required on Wayland |
| `--mirror-display D` | which display to capture (X11 `:0.0`, macOS index) |
| `--accept-input` | allow remote keyboard and pointer (off by default) |
| `--auto-clipboard` | push clipboard changes to connected devices (off by default) |
| `--notify-allow`, `--notify-block` | source-side notification filter, by app id |
| `--rendezvous`, `--relay`, `--stun` | reaching devices off the LAN |
| `--quiet` | log nothing but errors |

Everything that can expose this machine to another one is off unless you turn it on.

### Files, ports and paths

Keys and the trust store live in `~/.config/openair` (`%AppData%\openair` on Windows), overridable with `--keys`. The daemon's local socket is under `$XDG_RUNTIME_DIR/openair/openaird.sock`, or a per-user named pipe on Windows.

Open these if a firewall is in the way:

| Port | Why |
| --- | --- |
| **UDP 9000** | QUIC — the actual connection |
| **UDP 5353** | mDNS discovery |
| **UDP 53318** | discovery fallback, for networks that filter multicast |
| TCP/UDP 9443 | your rendezvous server, if you run one |
| TCP 9444 | your relay, if you run one |

On Linux, mDNS also wants Avahi: `sudo systemctl enable --now avahi-daemon`.

---

## Platform notes

**Linux.** Remote input goes through `/dev/uinput`, which is below the display server — so the same code drives X11, Wayland and a bare console — but it is root-only unless a udev rule says otherwise. Something like:

```
KERNEL=="uinput", GROUP="input", MODE="0660"
```

and put your user in the `input` group. A daemon started with `--accept-input` that cannot open the device says so at start-up.

**Wayland.** `ffmpeg` cannot open a desktop portal by itself, so screen sharing on a Wayland session needs `--mirror-command` naming a portal-capable producer that writes Annex-B H.264 to stdout.

**Windows.** No special privilege is needed for input injection, with one exception you cannot work around: a process running at a higher integrity level than the daemon ignores injected input entirely, silently. Run the daemon elevated if you need to drive an elevated window.

**macOS.** Screen capture and input injection both require the usual TCC permissions; grant them to the terminal or service that runs `openaird`.

---

## Troubleshooting

**"not paired" on a send.** Pairing is per device pair and both ends check it independently. Run `openair devices --paired` on both.

**A named device is not found.** `openair discover` shows what the network can see. If multicast is filtered, the unicast fallback on UDP 53318 needs to be open. Failing that, send to an explicit `host:port`.

**"run openair unlock first".** The command needs Owned. Check all three steps are done: `protect` on the device being reached, `trust DEVICE --owned` there, `unlock` here.

**Owned refused after `protect`.** Devices paired before you ran `protect` never received the privilege key. Pair them again.

**`ls` shows nothing.** The far daemon has no `--share`.

**Mirror refused.** The source needs `--share-screen` *and* an encoder. The error says which is missing.

**A player shows one decode error when joining a mirror.** It joined between keyframes; it recovers at the next one, at most two seconds later.

**The daemon is not reachable.** `openair status` will say so. Check `openaird` is running as the same user — the socket is per-user and owner-only, deliberately.

---

## Security model in brief

Identity is an Ed25519 keypair, and the device's name on the wire is derived from it. TLS 1.3 authenticates both ends against **pinned raw public keys** — no certificate authority, no hostname check, nothing an attacker can obtain by convincing a third party of something.

Pairing is the only moment a device will talk to a stranger, and it is bounded by a window you opened. The six digits both users compare are what stop a machine in the middle: it can relay the exchange, but it cannot make both screens agree.

Unattended access is a second keypair, sealed at rest with your passphrase, unsealed only for the length of an unlock, held in memory pages the kernel is asked not to swap, and zeroed the moment the session ends. An unlock authorises one specific device. Core dumps are disabled before the key is ever unsealed.

Discovery is treated as an unauthenticated hint and nothing more: a device announcing a DeviceID proves nothing, and the pinned-key handshake is what actually decides. Screen sharing and remote input are opt-in, show an indicator on the machine being accessed, and can be ended there.

`docs/threat-model.md` is the long version, including what it does *not* protect against.

---

## Documentation

| File | What is in it |
| --- | --- |
| `docs/PROTOCOL.md` | the normative wire specification |
| `docs/threat-model.md` | assets, boundaries, adversaries, accepted weaknesses |
| `docs/functionality.md` | what every file does, and the sharp edges |
| `docs/decision-tree.md` | every design decision, with its reasoning, in order |
| `docs/openair2-prd.md`, `docs/openair2-hld.md` | requirements and high-level design |

---

## License

GNU General Public License v3.0. Use it, change it, pass it on — derivative work stays open under the same terms. See `LICENSE`.

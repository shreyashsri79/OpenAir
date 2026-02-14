
# OpenAir 🌬️

**AirDrop-like serverless file sharing from Android to any OS (Linux / macOS / Windows)**

## Overview

**OpenAir** is a lightweight, serverless, LAN-based file sharing system that enables **Android → Laptop/Desktop** transfers across multiple operating systems.

Unlike AirDrop (Apple-only), OpenAir is designed to work across:

* **Linux** (Fedora, Ubuntu, Kali, Arch, etc.)
* **macOS**
* **Windows**

OpenAir uses:

* **mDNS (Zeroconf)** for device discovery
* **Direct TCP sockets** for file transfer
* **SHA-256** for integrity verification
* **Receiver-side Accept/Reject** for safety

No cloud. No storage server. No ecosystem lock-in.

---

## Key Features

### Android App (Sender)

* Discovers nearby devices automatically (mDNS)
* Lists available receivers on the same Wi-Fi network
* Select a file using Android file picker (SAF)
* Sends file with live progress

### Receiver App (Linux/macOS/Windows)

* Runs as a lightweight TCP receiver
* Shows incoming file request
* User can **Accept / Reject**
* Saves files to `~/Downloads/OpenAir/` (or equivalent Downloads folder)
* Verifies file integrity using **SHA-256**

---

## Why OpenAir?

* ✅ Works on local Wi-Fi (no internet required)
* ✅ No cloud upload
* ✅ No server required
* ✅ Cross-platform receiver support
* ✅ Fast transfer (LAN speed)
* ✅ Simple and reliable protocol

---

## Architecture (High Level)

### Discovery Layer

Receivers advertise themselves on LAN using mDNS:

* Service: `_openair._tcp.local`
* Port: `8989`
* Metadata: device name + version

Android scans and lists all receivers.

### Transfer Layer

Once selected, Android connects directly to the receiver via TCP:

* JSON header line
* Accept/Reject handshake
* Raw file byte stream
* SHA-256 verification

---

## Transfer Protocol

### 1) Sender → Receiver (Header)

Sender sends **one JSON line** ending with `\\n`:

```json
{
  "name": "photo.jpg",
  "size": 3482332,
  "sha256": "<64-char-hex>"
}
```

### 2) Receiver → Sender (Handshake)

Receiver replies:

* `ACCEPT\\n`
  or
* `REJECT\\n`

### 3) Sender → Receiver (File Stream)

If accepted, sender streams exactly `size` bytes.

### 4) Integrity Check

Receiver computes SHA-256 of received bytes and compares with header.

---

## Security Model (MVP)

OpenAir focuses on simple, practical, exhibition-ready security:

* **Receiver-side Accept/Reject**
* **SHA-256 integrity verification**
* No cloud exposure
* No background silent receiving

> Note: mDNS is used only for discovery, not for trust.

---

## Tech Stack

### Android

* Kotlin
* Jetpack Compose
* MVVM
* Storage Access Framework (SAF)
* Networking: TCP sockets + mDNS discovery

### Receiver (Cross-platform)

* Go
* TCP server
* Zeroconf/mDNS advertisement
* File saving + SHA-256 verification

---

## Getting Started

### Receiver (Linux/macOS)

#### 1) Install Go

Fedora:

```bash
sudo dnf install -y golang
```

Ubuntu/Kali:

```bash
sudo apt install -y golang
```

Arch:

```bash
sudo pacman -S go
```

macOS:

```bash
brew install go
```

#### 2) Run receiver

```bash
go run .
```

Or build:

```bash
go build -o openair-receiver
./openair-receiver
```

---

## Linux mDNS Requirement (for discovery)

For device discovery on Linux, ensure Avahi is enabled:

Fedora:

```bash
sudo dnf install -y avahi avahi-tools
sudo systemctl enable --now avahi-daemon
```

Ubuntu/Kali:

```bash
sudo apt install -y avahi-daemon avahi-utils
sudo systemctl enable --now avahi-daemon
```

Arch:

```bash
sudo pacman -S avahi
sudo systemctl enable --now avahi-daemon
```

---

## Firewall Notes

Receiver requires:

* TCP port `8989`
* UDP port `5353` (mDNS)

Fedora example:

```bash
sudo firewall-cmd --add-port=8989/tcp --permanent
sudo firewall-cmd --reload
```

---

## Demo / Exhibition Setup

Recommended setup:

* 1 Android phone
* Multiple machines (real or VMs):

  * Fedora
  * Ubuntu
  * Arch
  * Kali
  * macOS
  * Windows

Demo flow:

1. Start receiver on all machines
2. Open Android OpenAir app
3. Nearby devices appear automatically
4. Select multiple receivers
5. Send file simultaneously
6. Receivers accept and save

---

## Roadmap

* Multi-file + folder sending
* Android ↔ Android mode
* Resume support for large files
* Optional encryption (TLS / AES-GCM)
* Receiver tray UI for desktop platforms

---

## License

MIT (recommended for hackathons/exhibition projects)


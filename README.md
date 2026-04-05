# OpenAir

<p align="center">
  <img src="./logo.png" alt="OpenAir Logo" width="500" />
</p>

**High-performance, serverless, cross-platform file transfer using parallel data streaming**

---

## Overview

**OpenAir** is a lightweight, serverless, LAN-based file transfer system designed to achieve **maximum throughput by overcoming per-connection bandwidth limits**.

It enables fast file transfer among:

 **Android / Linux / macOS / Windows**

Unlike traditional tools, OpenAir uses **parallel TCP connections** to fully utilize available network bandwidth.

---

## Key Idea

Most networks limit **bandwidth per connection**, not total bandwidth.

OpenAir exploits this by:

```
splitting file → multiple chunks → multiple connections → bandwidth aggregation
```

Result:

**5–10× faster transfers in real-world conditions**

---

## Features

### Android

* Automatic device discovery via mDNS
* Multi-file selection (SAF)
* Parallel chunk streaming
* Real-time transfer logic
* No cloud / no intermediate storage

---

### Cross-Platform (Linux / macOS / Windows) (Go)

* Lightweight TCP server
* Accept/Reject handshake
* Concurrent connection handling
* Offset-based file reconstruction

---

## Architecture

### 1. Discovery Layer

* Protocol: **mDNS (Zeroconf)**
* Service: `_openair._tcp`
* Enables automatic LAN discovery

---

### 2. Control Connection

Single TCP connection used for:

* Metadata exchange
* Accept / Reject handshake
* RTT + bandwidth estimation

---

### 3. Data Layer (Core Innovation)

Instead of a single stream:

```
File → split into chunks → sent via multiple TCP connections
```

Each worker:

* Opens its own connection
* Sends chunk independently
* Operates concurrently

---

### 4. Chunk Model

Each chunk contains:

```
offset → where to write
size   → how much data
data   → actual bytes
```

Receiver uses:

```go
file.WriteAt(data, offset)
```

→ Enables **out-of-order parallel writes**

---

## Transfer Flow

### 1. Metadata Exchange

Sender → Receiver:

```json
{
  "name": "file.jpg",
  "size": 104857600,
  "timestamp": 123456789
}
```

---

### 2. Handshake

Receiver:

```
ACCEPT
```

* sends RTT probe response

---

### 3. Parallel Streaming

* Multiple workers start
* Each opens its own TCP connection
* Chunks are sent concurrently

---

### 4. Reconstruction

Receiver:

```
receives chunk → writes at offset → file builds in parallel
```

---

## Performance

Example (real test):

```
File Size: 99 MB
Expected (single stream): ~50 sec
OpenAir: ~3.4 sec
```

→ Achieved via **bandwidth aggregation across connections**

---

## Design Insights

OpenAir explores:

* Per-flow bandwidth throttling
* Parallel TCP streaming
* Backpressure and flow control
* Trade-offs between throughput and fairness
* Comparison with modern protocols (e.g., QUIC)

---

## Security Model (MVP)

* Receiver-side Accept / Reject
* No automatic file acceptance
* LAN-only communication
* No cloud exposure

> Note: Encryption and authentication are planned improvements

---

## Tech Stack

### Android

* Kotlin
* TCP sockets
* mDNS discovery

---

### Receiver

* Go
* net package (TCP)
* Zeroconf (mDNS)
* Concurrent worker model

---

## Getting Started

### Receiver

```bash
go run .
```

or

```bash
go build -o openair-receiver
./openair-receiver
```

---

## mDNS Setup (Linux)

```bash
sudo systemctl enable --now avahi-daemon
```

---

## Firewall

```
TCP: 9000
UDP: 5353 (mDNS)
```

---

## Demo Setup


Flow:

1. Start receiver
2. Discover via app
3. Select device
4. Send file
5. Observe parallel transfer

---


## License

This project is licensed under the **GNU General Public License v3.0 (GPL-3.0)**.

You are free to:

* Use
* Modify
* Distribute

But any derivative work must also be **open-sourced under GPL v3.0**.

See the LICENSE file for details.

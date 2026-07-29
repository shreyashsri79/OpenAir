# Windows baseline runbook (D-22 / D-23)

Measures what quic-go actually costs on Windows, where it falls back to
`basicConn`: no send offload, no batched receive (D-13, D-23). Everything so far
is inference from code plus a Linux proxy, and D-23 records that those numbers
are an *optimistic* bound because the Linux runs kept a batched receiver.

This is the **pre-fix baseline**. Without it there is no way to show afterwards
that the USO/URO work in ADR-8 achieved anything.

## What you need

- The Linux machine and the Windows laptop on the same network, ideally wired or
  5 GHz WiFi, with nothing else saturating the link.
- `oabench.exe` on Windows (build it with `make -C oabench winkit`, or
  `GOOS=windows GOARCH=amd64 go build -o oabench.exe .`).
- `oabench` on Linux.
- The Linux machine's IP: `ip -brief addr | grep -v LOOPBACK`.

Windows will prompt for firewall access on first run. **Allow it for private
networks** — QUIC needs inbound UDP, and a silent block looks exactly like
catastrophic packet loss.

## Why both directions

Send and receive are separate gaps in quic-go (D-23), and per the PRD's own
scenarios the Windows laptop is more often the *receiver* — S2 receives a build
artifact, S3 pulls files and views a mirror. Measuring only one direction would
answer the less important half.

## Phase A — Windows sends

On **Linux**:

```bash
./oabench serve -transport tcp -addr 0.0.0.0:9100
```

On **Windows**, replacing the IP:

```powershell
.\oabench.exe send -transport tcp -addr 192.168.1.50:9100 `
  -size 1GiB -streams 1,2,4,8 -runs 3 -label win-sender >> results-windows.jsonl
```

Stop the Linux server, restart it with `-transport quic`, and repeat the send
with `-transport quic`. Both transports must run against the same link within a
few minutes of each other, or you are comparing two different networks.

## Phase B — Windows receives

Swap the roles. On **Windows**:

```powershell
.\oabench.exe serve -transport tcp -addr 0.0.0.0:9100
```

On **Linux**:

```bash
./oabench serve -transport tcp   # stop this first if still running
./oabench send -transport tcp -addr <WINDOWS_IP>:9100 \
  -size 1GiB -streams 1,2,4,8 -runs 3 -label win-receiver >> results-windows.jsonl
```

Repeat for `quic`.

## Reading the result

The number that matters is **QUIC as a fraction of TCP on the same link**, in
each direction. PRD G1 makes parity the bar for Windows.

- Above ~85% in both directions: ADR-8 is a lower priority than assumed and the
  fork carries two patches instead of three.
- Around 50%, matching the Linux GSO-off proxy: ADR-8 is confirmed and should be
  scheduled into Phase 1.
- Worse when Windows receives than when it sends: the receive gap dominates,
  which D-23 predicts and which would make URO the more urgent half.

Also record `cpu_sec_per_gib`. If Windows QUIC is CPU-bound rather than
link-bound the throughput figure understates the problem, because a faster link
will not help.

This run also supplies the **two-machine data** outstanding since D-4, whose
single-box co-location caveat has qualified every throughput number so far.
Note the link type and speed alongside the results — without netem there is no
shaping here, so the physical link is the profile.

# Windows baseline — single machine (D-22 / D-23 / D-32)

Runs entirely on one Windows machine. No second host, no network emulation, no
admin rights, no firewall prompts.

## Exactly what to do

1. Copy these three files to any folder on the Windows machine:
   - `RUN-ME.bat`
   - `run-baseline.ps1`
   - `oabench-amd64.exe` (or `oabench-arm64.exe` on an ARM device — the script
     picks automatically, so copying both is fine)

2. **Double-click `RUN-ME.bat`.**

3. Wait 3–6 minutes. Close heavy applications first; this measurement is
   CPU-bound by design, so a background build or a browser will skew it.

4. When it finishes it prints the results between two markers. Copy everything
   between `--- copy from here ---` and `--- to here ---` and paste it back.
   The same text is saved as `results-windows.jsonl` next to the script.

That is all. If Windows SmartScreen objects to the `.exe` because it arrived via
a browser or email, right-click it → Properties → tick **Unblock**, or run
`Unblock-File .\oabench-amd64.exe` in PowerShell. If the run reports no results,
the usual cause is port 9300 already being in use; the script prints the server
log when that happens.

## Why loopback, and why that is the right test

The Windows question is **per-packet syscall cost**. quic-go falls back to
`basicConn` there: no send offload (`UDP_SEND_MSG_SIZE` unused) and no batched
receive, so it makes one syscall per packet in both directions (D-13, D-23).

On loopback there is no network bottleneck, so throughput is bounded by exactly
that cost — which makes loopback the *sharpest* instrument for this question
rather than a compromise. quic-go also paces at a fixed ~1200-byte packet size
regardless of link MTU, so the QUIC figures compare directly against Linux.

Everything measured so far has been inference from reading the code, plus a
Linux proxy (`QUIC_GO_DISABLE_GSO=1`). D-23 records that the proxy is an
*optimistic* bound, because those Linux runs kept a batched receiver while a
real Windows machine has neither half. This run replaces inference with a
number.

## Linux reference, measured 2026-07-30

Same binary, same loopback, same 512 MiB payload, median of 2 runs, on the
development machine:

| config | 1 stream | 4 streams | CPU s/GiB |
|---|---|---|---|
| TCP | 14410 Mb/s | 32609 Mb/s | 0.5–0.9 |
| QUIC, GSO **on** | 2307 Mb/s | 2248 Mb/s | 6.4–6.6 |
| QUIC, GSO **off** | 692 Mb/s | 688 Mb/s | 26.0–26.7 |

Disabling GSO costs **3.3× throughput and 4× CPU**. That is the effect ADR-8
exists to remove.

## What the result will mean

Compare your **QUIC** numbers against the GSO-off row. TCP is included for
context but is not comparable across the two operating systems — loopback MTU
differs, so TCP uses much larger segments and the figure is inflated on both.

- **Windows QUIC ≈ 690 Mb/s, ~26 CPU s/GiB** — the Linux proxy was accurate. The
  send path is the whole story, and D-22's USO work is the fix.
- **Windows QUIC materially worse** — D-23's prediction is confirmed and
  quantified: the missing batched receive costs real throughput on top of the
  missing send offload, making URO as important as USO.
- **Windows QUIC close to the GSO-on row** — something in the Windows stack is
  compensating, ADR-8 drops sharply in priority, and the vendored fork carries
  one patch rather than three.

Record the CPU column either way. If Windows QUIC is CPU-bound rather than
link-bound, a faster network will not help and the number understates the
problem.

## Later: the two-machine run

Deferred to Phase 2 by D-32, but still a hard Phase 1 *exit* blocker, since PRD
G1's parity bar cannot be claimed for a platform never measured on a real link.
When both machines are free, run `oabench serve` on one and `oabench send` on
the other, in **both directions** — Windows as sender and as receiver — because
those are separate gaps (D-23) and the PRD's Windows laptop is more often the
receiver.

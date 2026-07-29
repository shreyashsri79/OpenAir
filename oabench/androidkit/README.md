# Android on-device runbook (D-31 follow-up)

D-31 settled that gomobile binds quic-go and what it costs in size. It did not
settle **runtime**: throughput on real phone hardware, and CPU cost, which is the
live risk against PRD R30's battery budget after D-4 measured QUIC at 10–20x
TCP's CPU per byte.

This runs the transport itself on the phone. It deliberately does **not** wrap
the AAR in an app — Android is Linux, so `oabench` runs directly via `adb`, and
the question here is how the transport behaves on that CPU and that radio, not
whether Compose can call it.

This also supplies the **two-machine data** outstanding since D-4, whose
single-box co-location caveat qualifies every throughput figure recorded so far.
Phone and desktop are genuinely two machines with two CPUs.

## Setup

```bash
export ADB=~/Android/Sdk/platform-tools/adb
make -C oabench androidkit          # builds androidkit/oabench-android-arm64

$ADB devices                        # enable USB debugging on the phone first
$ADB push oabench/androidkit/oabench-android-arm64 /data/local/tmp/oabench
$ADB shell chmod 755 /data/local/tmp/oabench
```

Phone and desktop on the **same WiFi**. Get both addresses:

```bash
ip -brief addr | grep -v LOOPBACK            # desktop
$ADB shell ip -f inet addr show wlan0        # phone
```

**Keep the phone plugged in for the throughput runs.** WiFi power saving and
thermal throttling will otherwise dominate the result and you will be measuring
the phone's power policy rather than the transport. Do the battery run
separately and unplugged (below).

## Phase A — phone sends

Desktop:

```bash
./oabench/oabench serve -transport tcp -addr 0.0.0.0:9100
```

Phone:

```bash
$ADB shell /data/local/tmp/oabench send -transport tcp \
  -addr <DESKTOP_IP>:9100 -size 512MiB -streams 1,2,4,8 -runs 3 \
  -label phone-sender
```

Restart the desktop server with `-transport quic` and repeat with
`-transport quic`. Run both transports within a few minutes of each other or
you are comparing two different radio conditions.

## Phase B — phone receives

This is the direction that matters most for battery: receiving a large transfer
is S2 and S5, and it is where a phone spends real energy.

```bash
$ADB shell /data/local/tmp/oabench serve -transport tcp -addr 0.0.0.0:9100
```

Desktop:

```bash
./oabench/oabench send -transport tcp -addr <PHONE_IP>:9100 \
  -size 512MiB -streams 1,2,4,8 -runs 3 -label phone-receiver
```

Repeat for `quic`.

## Optional — battery

Unplugged, screen off is fine for a foreground `adb shell` process:

```bash
$ADB shell dumpsys batterystats --reset
# run one large QUIC transfer, then one large TCP transfer
$ADB shell dumpsys batterystats | grep -A5 "Estimated power use"
```

Rough, but the QUIC-versus-TCP ratio is what matters, and both runs see the same
measurement error.

## Reading the result

- **`cpu_sec_per_gib` is the number for R30.** On the desktop QUIC cost 10–20x
  TCP. If that ratio holds on a phone, sustained transfers are a battery problem
  and it feeds back into ADR-5 and the D-14 congestion-control work.
- **Throughput** as a fraction of TCP on the same link tells you whether the
  Linux-desktop findings transfer to a mobile CPU.
- **Phase B against Phase A.** A phone that receives much worse than it sends
  points at receive-path cost, which is the same asymmetry D-23 found on Windows.

Android compiles quic-go's Linux GSO path (`GOOS=android` satisfies the `linux`
build tag, verified in D-13), so unlike Windows there is no send-offload gap
here — any shortfall is CPU or radio, not a missing syscall.

#!/usr/bin/env bash
# lab.sh <profile> -- <command...>
#
# Runs a command inside a private network namespace whose loopback is shaped to
# a named profile. Uses an unprivileged user namespace, so no root and no sudo:
# you are root *inside* the namespace, which is enough for `ip` and `tc`.
#
# Loopback rather than a veth pair is a deliberate tradeoff. A veth setup is
# more faithful but needs real root to move an interface between namespaces.
# Shaped loopback still exercises the full UDP socket path including
# UDP_SEGMENT (quic-go's GSO), which is the thing under test. See README
# "What Linux can and cannot answer".
set -euo pipefail

if [[ $# -lt 1 ]]; then
  echo "usage: $0 <profile> -- <command...>" >&2
  echo "profiles: none lan-1g wifi-5g hotspot wan-relay cgnat-punch" >&2
  exit 2
fi

profile=$1; shift
[[ ${1:-} == "--" ]] && shift

# RTT is round-trip; netem delays each traversal once, so each direction gets
# half. Rates and losses are chosen to bracket the conditions the PRD cares
# about, with wifi-5g anchored on v1.0's published 148 Mb/s test environment.
case $profile in
  none)        RATE_MBIT=0;    RTT_MS=0;   LOSS="" ;;
  lan-1g)      RATE_MBIT=1000; RTT_MS=1;   LOSS=0 ;;
  wifi-5g)     RATE_MBIT=200;  RTT_MS=6;   LOSS=0.1 ;;
  hotspot)     RATE_MBIT=600;  RTT_MS=4;   LOSS=0.05 ;;
  wan-relay)   RATE_MBIT=100;  RTT_MS=80;  LOSS=0.5 ;;
  cgnat-punch) RATE_MBIT=50;   RTT_MS=120; LOSS=1 ;;
  *) echo "unknown profile: $profile" >&2; exit 2 ;;
esac

DELAY_MS=$(awk "BEGIN{printf \"%.3f\", $RTT_MS/2}")
RATE="${RATE_MBIT}mbit"

# Queue depth, in packets, derived from the bandwidth-delay product rather than
# fixed. This matters more than it looks: netem's default of 1000 packets
# manufactures loss on high-BDP paths, but an arbitrarily large value produces
# absurd bufferbloat instead -- an earlier revision used 100000 packets, roughly
# 150 MB, which inflated latency-under-load by more than an order of magnitude
# and told us about the lab rather than the transport. BUFFER_BDP multiples of
# the BDP is the realistic middle; 4 is a common figure for consumer equipment.
# Override with BUFFER_BDP=1 for a shallow-buffered link, or a larger value to
# study bufferbloat deliberately.
BUFFER_BDP=${BUFFER_BDP:-4}
LIMIT=$(awk "BEGIN{
  bdp_bytes = $RATE_MBIT * 1000000 / 8 * ($RTT_MS / 1000);
  pkts = $BUFFER_BDP * bdp_bytes / 1500;
  if (pkts < 64) pkts = 64;
  printf \"%d\", pkts
}")

setup="
ip link set lo up
ip link set lo mtu 1500
sysctl -qw net.core.rmem_max=16777216 >/dev/null 2>&1 || true
sysctl -qw net.core.wmem_max=16777216 >/dev/null 2>&1 || true
"
if [[ $RATE_MBIT -gt 0 ]]; then
  setup+="
tc qdisc add dev lo root netem rate $RATE delay ${DELAY_MS}ms loss ${LOSS}% limit $LIMIT
"
fi

export OA_PROFILE=$profile
exec unshare -Urn bash -c "$setup
exec \"\$@\"" bash "$@"

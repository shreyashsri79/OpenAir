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
  none)        RATE="";       RTT_MS=0;   LOSS="" ;;
  lan-1g)      RATE=1gbit;    RTT_MS=1;   LOSS=0 ;;
  wifi-5g)     RATE=200mbit;  RTT_MS=6;   LOSS=0.1 ;;
  hotspot)     RATE=600mbit;  RTT_MS=4;   LOSS=0.05 ;;
  wan-relay)   RATE=100mbit;  RTT_MS=80;  LOSS=0.5 ;;
  cgnat-punch) RATE=50mbit;   RTT_MS=120; LOSS=1 ;;
  *) echo "unknown profile: $profile" >&2; exit 2 ;;
esac

DELAY_MS=$(awk "BEGIN{printf \"%.3f\", $RTT_MS/2}")

setup="
ip link set lo up
ip link set lo mtu 1500
sysctl -qw net.core.rmem_max=16777216 >/dev/null 2>&1 || true
sysctl -qw net.core.wmem_max=16777216 >/dev/null 2>&1 || true
"
if [[ -n $RATE ]]; then
  # limit is raised well above netem's 1000-packet default: at these rates the
  # default queue overflows and manufactures loss that isn't part of the profile.
  setup+="
tc qdisc add dev lo root netem rate $RATE delay ${DELAY_MS}ms loss ${LOSS}% limit 100000
"
fi

export OA_PROFILE=$profile
exec unshare -Urn bash -c "$setup
exec \"\$@\"" bash "$@"

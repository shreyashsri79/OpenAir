#!/usr/bin/env bash
# report.sh [results.jsonl]
#
# Pivots the matrix into the two tables that actually decide ADR-1: throughput
# per transport, and the QUIC-vs-TCP ratio against the 15% gate in PRD R12/G2.
set -euo pipefail

FILE=${1:-$(cd "$(dirname "$0")" && pwd)/results.jsonl}

if ! command -v jq >/dev/null; then
  echo "jq not installed; raw results are in $FILE" >&2
  exit 0
fi

echo
echo "throughput (Mb/s) and sender CPU (s/GiB)"
printf '%-12s %-6s %-4s %-8s %10s %10s\n' PROFILE TRANS GSO STREAMS MBPS CPU_S_GIB
jq -r '[.profile // "none", .transport, .gso, (.streams|tostring),
        (.mbps|floor|tostring), (.cpu_sec_per_gib*100|floor/100|tostring)] | @tsv' "$FILE" |
  while IFS=$'\t' read -r prof trans gso streams mbps cpu; do
    printf '%-12s %-6s %-4s %-8s %10s %10s\n' "$prof" "$trans" "$gso" "$streams" "$mbps" "$cpu"
  done

echo
echo "gate check: best QUIC vs best TCP per profile (PRD R12 wants >= 85%)"
printf '%-12s %12s %12s %12s %8s\n' PROFILE TCP_BEST QUIC_GSO_ON QUIC_GSO_OFF VERDICT
jq -rs '
  group_by(.profile // "none")[] |
  {
    profile: (.[0].profile // "none"),
    tcp:     ([.[] | select(.transport=="tcp")     | .mbps] | max // 0),
    quic_on: ([.[] | select(.transport=="quic" and .gso=="on")  | .mbps] | max // 0),
    quic_off:([.[] | select(.transport=="quic" and .gso=="off") | .mbps] | max // 0)
  } |
  [ .profile,
    (.tcp|floor|tostring),
    (.quic_on|floor|tostring),
    (.quic_off|floor|tostring),
    (if .tcp == 0 then "n/a"
     elif (.quic_off / .tcp) >= 0.85 then "PASS"
     else ((.quic_off / .tcp * 100)|floor|tostring) + "%" end) ] | @tsv' "$FILE" |
  while IFS=$'\t' read -r prof tcp qon qoff verdict; do
    printf '%-12s %12s %12s %12s %8s\n' "$prof" "$tcp" "$qon" "$qoff" "$verdict"
  done
echo
echo "VERDICT column compares GSO-off QUIC (the Windows proxy) against best TCP."

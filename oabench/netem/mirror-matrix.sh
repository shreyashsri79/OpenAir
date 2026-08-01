#!/usr/bin/env bash
# mirror-matrix.sh [profile...] -- the ADR-4 / D-9 spike.
#
# Runs both framings -- stream-per-frame with RESET_STREAM (PROTOCOL.md §14.2)
# and datagrams with application-level fragmentation (ADR-4 option A) -- across
# shaped profiles, alone and against a saturating bulk transfer on the same
# connection, which is the case D-24 says decides it.
#
# Results are JSON Lines on stdout, human summary on stderr.
set -euo pipefail

here=$(cd "$(dirname "$0")" && pwd)
root=$(dirname "$here")
bin="$root/oabench"
[[ -x $bin ]] || { echo "build oabench first: (cd $root && go build -o oabench .)" >&2; exit 2; }

profiles=("$@")
[[ ${#profiles[@]} -eq 0 ]] && profiles=(lan-1g wifi-5g wan-relay)

SECONDS_PER_RUN=${SECONDS_PER_RUN:-20}
FPS=${FPS:-60}
BITRATE=${BITRATE:-8Mb}
PORT=${PORT:-9400}

for profile in "${profiles[@]}"; do
  for mode in stream datagram; do
    for contention in none bulk bulk-quiesce; do
      args=(-mode "$mode" -fps "$FPS" -bitrate "$BITRATE" -seconds "$SECONDS_PER_RUN" -profile "$profile")
      case $contention in
        bulk)         args+=(-bulk) ;;
        bulk-quiesce) args+=(-bulk -quiesce) ;;
      esac
      args+=(-label "$contention")

      echo "== $profile $mode $contention" >&2
      # One namespace per run holds both ends, so the sink and source share a
      # clock and the shaping applies to the path between them.
      "$here/lab.sh" "$profile" -- bash -c "
        '$bin' mirror-serve -addr 127.0.0.1:$PORT >/dev/null 2>&1 &
        sink=\$!
        sleep 1
        '$bin' mirror -addr 127.0.0.1:$PORT ${args[*]}
        kill \$sink 2>/dev/null || true
      "
    done
  done
done

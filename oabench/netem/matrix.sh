#!/usr/bin/env bash
# matrix.sh [profile ...]
#
# Runs the full week-1 transport matrix and appends JSON Lines to results.jsonl.
#
#   ./netem/matrix.sh                      # default profiles
#   SIZE=1GiB RUNS=5 ./netem/matrix.sh wifi-5g
#
# Env: SIZE, STREAMS, RUNS, CHUNK, OUT, OABENCH_BIN
set -euo pipefail

HERE=$(cd "$(dirname "$0")" && pwd)
BIN=${OABENCH_BIN:-$HERE/../oabench}
SIZE=${SIZE:-512MiB}
STREAMS=${STREAMS:-1,2,4,8,16}
RUNS=${RUNS:-3}
OUT=${OUT:-$HERE/results.jsonl}

if [[ ! -x $BIN ]]; then
  echo "building oabench..." >&2
  (cd "$HERE/.." && go build -o "$BIN" .)
fi

profiles=("$@")
if [[ ${#profiles[@]} -eq 0 ]]; then
  profiles=(lan-1g wifi-5g wan-relay)
fi

: > "$OUT"
failed=()
for p in "${profiles[@]}"; do
  echo "### profile: $p" >&2
  # A failing profile must not abandon the remaining ones -- a partial matrix
  # that looks complete is worse than one that says which rows are missing.
  if ! "$HERE/lab.sh" "$p" -- "$HERE/run-one.sh" "$BIN" "$p" "$SIZE" "$STREAMS" "$RUNS" >> "$OUT"; then
    failed+=("$p")
  fi
done

if [[ ${#failed[@]} -gt 0 ]]; then
  echo >&2
  echo "!! incomplete profiles: ${failed[*]}" >&2
fi

echo >&2
echo "wrote $OUT" >&2
"$HERE/report.sh" "$OUT"

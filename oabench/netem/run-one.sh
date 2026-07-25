#!/usr/bin/env bash
# run-one.sh <oabench-binary> <profile> <size> <stream-list> <runs>
#
# Executes one full transport sweep. Expected to be run *inside* lab.sh, where
# loopback is already shaped. Emits JSON Lines on stdout; progress on stderr.
set -uo pipefail

BIN=$1; PROFILE=$2; SIZE=$3; STREAMS=$4; RUNS=$5
ADDR=127.0.0.1:9100
export QUIC_GO_DISABLE_RECEIVE_BUFFER_WARNING=1

failures=0

sweep() {
  local transport=$1 gso_label=$2

  "$BIN" serve -transport "$transport" -addr "$ADDR" >/dev/null 2>&1 &
  local pid=$!
  # Give the listener a moment; on a shaped link an early dial just fails.
  sleep 0.7

  echo "== $PROFILE / $transport / gso=$gso_label ==" >&2
  "$BIN" send -transport "$transport" -addr "$ADDR" \
    -size "$SIZE" -streams "$STREAMS" -runs "$RUNS" -profile "$PROFILE"
  local rc=$?
  if [[ $rc -ne 0 ]]; then
    echo "!! $PROFILE / $transport / gso=$gso_label failed (rc=$rc)" >&2
    failures=$((failures + 1))
  fi

  # Terminating the server makes `wait` report the signal. Swallow it
  # explicitly: an unguarded non-zero here propagates out through lab.sh's
  # exec and trips `set -e` in matrix.sh, which silently truncates the run
  # after the first profile.
  kill "$pid" 2>/dev/null || true
  wait "$pid" 2>/dev/null || true
  return 0
}

# TCP first: it is the baseline the candidate has to beat. GSO is irrelevant to
# TCP here -- the kernel handles segmentation either way.
unset QUIC_GO_DISABLE_GSO
sweep tcp "n/a"

# QUIC with kernel segmentation offload, the best case Linux offers.
unset QUIC_GO_DISABLE_GSO
sweep quic "on"

# QUIC without it. This is the closest a Linux box gets to the Windows send
# path, and is the actual answer to PRD risk K1.
export QUIC_GO_DISABLE_GSO=1
sweep quic "off"

if [[ $failures -gt 0 ]]; then
  echo "!! $PROFILE: $failures sweep(s) failed" >&2
  exit 1
fi
exit 0

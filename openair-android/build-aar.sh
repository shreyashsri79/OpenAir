#!/usr/bin/env bash
# Build the gomobile-bound Go core into app/libs/openair.aar.
#
# The Android shell (com.example.openair.v2) drives the same Go code the desktop
# CLI runs; this is what turns it into something Gradle can depend on (D-10,
# D-31). The .aar is a build artifact and is not in version control — run this
# after a fresh clone, and after any change under mobile/ or internal/.
#
# Requirements:
#   - Go toolchain (the repo's go.mod pins the version)
#   - Android SDK with an NDK installed
#   - $ANDROID_HOME, or an sdk.dir line in openair-android/local.properties
#
# gomobile itself is fetched on demand; the golang.org/x/mobile dependency is
# already recorded in the root go.mod as a tool directive, which is what makes
# `gomobile bind` willing to run against this module at all.
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
out="$repo_root/openair-android/app/libs/openair.aar"

# ABIs to build. arm64 covers every phone shipped in the last several years;
# amd64 is what the emulator runs on a desktop. Each ABI adds roughly 7 MB, so
# build the pair you actually need rather than all four.
targets="${OPENAIR_AAR_TARGETS:-android/arm64,android/amd64}"

# Minimum API. 24 is gomobile's floor for the runtime it links.
api="${OPENAIR_AAR_API:-24}"

if [[ -z "${ANDROID_HOME:-}" ]]; then
  props="$repo_root/openair-android/local.properties"
  if [[ -f "$props" ]]; then
    ANDROID_HOME="$(sed -n 's/^sdk\.dir=//p' "$props" | head -1)"
    export ANDROID_HOME
  fi
fi
if [[ -z "${ANDROID_HOME:-}" || ! -d "$ANDROID_HOME" ]]; then
  echo "build-aar: set ANDROID_HOME, or put sdk.dir=... in openair-android/local.properties" >&2
  exit 1
fi

if [[ -z "${ANDROID_NDK_HOME:-}" ]]; then
  # Newest installed NDK. gomobile needs one and does not pick for itself.
  ANDROID_NDK_HOME="$(find "$ANDROID_HOME/ndk" -maxdepth 1 -mindepth 1 -type d 2>/dev/null | sort -V | tail -1)"
  export ANDROID_NDK_HOME
fi
if [[ -z "${ANDROID_NDK_HOME:-}" || ! -d "$ANDROID_NDK_HOME" ]]; then
  echo "build-aar: no NDK found under $ANDROID_HOME/ndk; install one from the SDK manager" >&2
  exit 1
fi

export PATH="$PATH:$(go env GOPATH)/bin"
if ! command -v gomobile >/dev/null 2>&1; then
  echo "build-aar: installing gomobile"
  go install golang.org/x/mobile/cmd/gomobile@latest
fi

mkdir -p "$(dirname "$out")"
cd "$repo_root"

echo "build-aar: binding ./mobile for $targets (api $api)"
gomobile bind \
  -target="$targets" \
  -androidapi "$api" \
  -o "$out" \
  ./mobile

echo "build-aar: wrote $out ($(du -h "$out" | cut -f1))"

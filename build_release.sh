#!/usr/bin/env bash
# Build the shippable artifacts into release/: the four network binaries, the
# desktop window, and the Android APK.
#
# Nothing here is a gate. CI builds and tests the Go tree on every push; this
# script exists to produce the things a person installs, which need a display
# toolchain (Fyne) and an Android SDK (gomobile + Gradle) that CI does not have.
set -euo pipefail

WORKSPACE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
RELEASE_DIR="$WORKSPACE/release"

mkdir -p "$RELEASE_DIR/bin" "$RELEASE_DIR/gui" "$RELEASE_DIR/android"

# On Linux, go-gl/glfw compiles both the X11 and the Wayland backend unless a
# build tag picks one, so a machine without Wayland development headers fails on
# a header it did not ask for. X11 runs under XWayland on a Wayland session.
GUI_TAGS="${OPENAIR_GUI_TAGS:-}"
if [[ "$(go env GOOS)" == "linux" && -z "$GUI_TAGS" ]]; then
  GUI_TAGS="x11"
fi

echo "=== the daemon and the CLI ==="
cd "$WORKSPACE"
go build -o "$RELEASE_DIR/bin/" \
  ./cmd/openair ./cmd/openaird ./cmd/openair-relay ./cmd/openair-rendezvous
echo "wrote $(ls "$RELEASE_DIR/bin" | tr '\n' ' ')"

echo
echo "=== the desktop window ==="
# Not cross-compiled: Fyne needs cgo and the host's GL/X11 headers, so a
# cross-build needs fyne-cross and a container per target. Build for this
# machine here; run fyne-cross by hand for the others.
if go build ${GUI_TAGS:+-tags "$GUI_TAGS"} -o "$RELEASE_DIR/gui/openair-gui" ./cmd/openair-gui; then
  echo "wrote $RELEASE_DIR/gui/openair-gui"
else
  echo "openair-gui did not build — install the OpenGL/X11 development packages (see README)" >&2
fi

echo
echo "=== the Android APK ==="
if [[ ! -d "$WORKSPACE/openair-android" ]]; then
  echo "no openair-android directory; skipping" >&2
  exit 0
fi

# The .aar is the Go core bound with gomobile. It is a build artifact and is not
# in VCS, so it has to be built before Gradle can resolve the app's dependency
# on it.
if ! "$WORKSPACE/openair-android/build-aar.sh"; then
  echo "the gomobile bind failed; skipping the APK" >&2
  exit 0
fi

cd "$WORKSPACE/openair-android"
chmod +x gradlew

# Debug by default, because a debug APK is signed with the debug key and will
# install. A release build needs keystore.properties, which is deliberately not
# in VCS; without it the APK is unsigned and Android refuses it.
BUILD_TYPE="${OPENAIR_APK_BUILD:-debug}"
if [[ "$BUILD_TYPE" == "release" ]]; then
  ./gradlew assembleRelease -x lint -x lintVitalAnalyzeRelease
  out="app/build/outputs/apk/release"
  if [[ -f "$out/app-release.apk" ]]; then
    cp "$out/app-release.apk" "$RELEASE_DIR/android/openair-android-release.apk"
    echo "wrote $RELEASE_DIR/android/openair-android-release.apk (signed)"
  elif [[ -f "$out/app-release-unsigned.apk" ]]; then
    cp "$out/app-release-unsigned.apk" "$RELEASE_DIR/android/openair-android-release-unsigned.apk"
    echo "wrote an UNSIGNED release APK; it will not install. Add keystore.properties." >&2
  fi
else
  ./gradlew assembleDebug
  cp app/build/outputs/apk/debug/app-debug.apk "$RELEASE_DIR/android/openair-android-debug.apk"
  echo "wrote $RELEASE_DIR/android/openair-android-debug.apk"
fi

echo
echo "=== done — artifacts are in $RELEASE_DIR ==="

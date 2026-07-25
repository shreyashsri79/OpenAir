#!/bin/bash
set -e

WORKSPACE="/home/shreyashneeraj/Work/OpenAir"
RELEASE_DIR="$WORKSPACE/release"

mkdir -p "$RELEASE_DIR/gui"
mkdir -p "$RELEASE_DIR/android"

echo "========================================="
echo "   Building OpenAir GUI (Cross-Platform)"
echo "========================================="
cd "$WORKSPACE/openair-gui"

if ! command -v fyne-cross &> /dev/null; then
    echo "Installing fyne-cross..."
    go install github.com/fyne-io/fyne-cross@latest
    export PATH=$PATH:$(go env GOPATH)/bin
fi

echo "Building GUI for Linux..."
cp ../logo.png .
fyne-cross linux -arch=amd64,arm64 -icon=logo.png -app-id=com.openair.gui -env "GOTOOLCHAIN=auto"

echo "Building GUI for Windows..."
fyne-cross windows -arch=amd64 -icon=logo.png -app-id=com.openair.gui -env "GOTOOLCHAIN=auto"

echo "Building GUI for macOS..."
echo "Skipping macOS GUI build: Apple SDK is required and cannot be automatically distributed."
# fyne-cross darwin -arch=amd64,arm64 -icon=logo.png -app-id=com.openair.gui -env "GOTOOLCHAIN=auto"

echo "Packaging GUI binaries..."
cd fyne-cross/bin

for os_dir in linux-amd64 linux-arm64 windows-amd64 darwin-amd64 darwin-arm64; do
    if [ -d "$os_dir" ]; then
        binary=$(ls "$os_dir" | head -n 1)
        if [[ "$os_dir" == *"windows"* ]]; then
            zip -j "$RELEASE_DIR/gui/openair-gui-${os_dir}.zip" "$os_dir/$binary" > /dev/null
        else
            tar -czvf "$RELEASE_DIR/gui/openair-gui-${os_dir}.tar.gz" -C "$os_dir" "$binary" > /dev/null
        fi
    fi
done

echo ""
echo "========================================="
echo "   Building OpenAir Android APK"
echo "========================================="
cd "$WORKSPACE/openair-android"
if [ -f "gradlew" ]; then
    chmod +x gradlew
    ./gradlew assembleRelease -x lintVitalAnalyzeRelease -x lint
    
    APK_DIR="app/build/outputs/apk/release"
    if [ -f "$APK_DIR/app-release.apk" ]; then
        cp "$APK_DIR/app-release.apk" "$RELEASE_DIR/android/openair-android-release.apk"
        echo "Android APK (signed) compiled and packaged successfully."
    elif [ -f "$APK_DIR/app-release-unsigned.apk" ]; then
        cp "$APK_DIR/app-release-unsigned.apk" "$RELEASE_DIR/android/openair-android-release-unsigned.apk"
        echo "Warning: APK is unsigned (no keystore.properties found) — it will not install on devices."
    else
        echo "Warning: APK was not found in $APK_DIR"
    fi
else
    echo "Warning: gradlew not found in openair-android"
fi

echo ""
echo "========================================="
echo "   Build Complete!"
echo "   Artifacts saved to $RELEASE_DIR"
echo "========================================="

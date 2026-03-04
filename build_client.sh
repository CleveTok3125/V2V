#!/bin/sh

mkdir -p public

BUILD_ENV="CGO_ENABLED=0"

if [ -z "$APP_VERSION" ]; then
    APP_VERSION=$(git describe --tags --always 2>/dev/null || git rev-parse --short HEAD 2>/dev/null || echo "unknown")
fi

LDFLAGS="-s -w -X 'main.Version=${APP_VERSION}'"

PLATFORMS=(
    "windows/amd64"
    "windows/arm64"
    "linux/amd64"
    "linux/arm64"
    "android/arm64"
    "darwin/amd64"
    "darwin/arm64"
)

for PLATFORM in "${PLATFORMS[@]}"; do
    GOOS=${PLATFORM%/*}
    GOARCH=${PLATFORM#*/}

    OUTPUT_NAME="public/V2V-${GOOS}-${GOARCH}"

    if [ "$GOOS" = "android" ] && [ "$GOARCH" = "arm64" ]; then
        OUTPUT_NAME="public/V2V-android-aarch64"
    fi
    if [ "$GOOS" = "windows" ]; then
        OUTPUT_NAME+=".exe"
    fi

    echo "Building: $GOOS/$GOARCH..."

    env $BUILD_ENV GOOS=$GOOS GOARCH=$GOARCH go build -ldflags="$LDFLAGS" -o $OUTPUT_NAME ./client

    if [ $? -ne 0 ]; then
        echo "Error while building $GOOS/$GOARCH"
        exit 1
    fi
done

echo "Done!"

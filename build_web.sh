#!/usr/bin/sh
# Build the Go WASM client bundle used by the /web/ terminal page.
# Requires: go (with the js/wasm toolchain) and a writable GOCACHE.
set -e

cd "$(dirname "$0")"

GIT_HASH=$(git rev-parse --short HEAD 2>/dev/null || echo dev)

mkdir -p webterm
cp "$(go env GOROOT)/lib/wasm/wasm_exec.js" webterm/wasm_exec.js
printf 'window.V2V_VERSION = "%s";\n' "$GIT_HASH" > webterm/version.js

GOOS=js GOARCH=wasm go build -ldflags='-s -w' -o webterm/app.wasm ./client

echo "✅ webterm built (hash $GIT_HASH, $(wc -c < webterm/app.wasm) bytes)"
#!/usr/bin/sh
# Build the v2v-admin identity management tool.
#
# Usage:
#   ./build_admin.sh [output-path]      # defaults to ./v2v-admin
#
# Cross-compile by exporting GOOS/GOARCH first, e.g.:
#   GOOS=linux GOARCH=arm64 ./build_admin.sh v2v-admin-linux-arm64

set -e
cd "$(dirname "$0")"

OUT="${1:-./v2v-admin}"

go build -trimpath -ldflags '-s -w' -o "$OUT" ./cmd/v2v-admin

echo "✅ v2v-admin built: $OUT ($(wc -c < "$OUT") bytes)"

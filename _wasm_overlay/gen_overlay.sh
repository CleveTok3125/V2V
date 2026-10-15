#!/bin/sh
# Emit a Go build overlay that injects the js/wasm stubs into the vendored
# Bubble Tea v2 module.
#
# Bubble Tea v2 has no js/wasm build tags for the platform functions
# initInput, suspendSupported, suspendProcess and listenForResize, so a
# js/wasm build fails without them. Go refuses overlays beneath GOMODCACHE,
# so the mapping targets the vendor copy: build with -mod=vendor.
#
# Usage: go build -mod=vendor -overlay=<(gen_overlay.sh) ...
# The overlay path is absolute and machine-specific, so it is generated at
# build time and never committed.
set -e

root="$(cd "$(dirname "$0")/.." && pwd)"
bt="$root/vendor/charm.land/bubbletea/v2"
cb="$root/vendor/github.com/atotto/clipboard"

cat <<EOF
{
  "Replace": {
    "$bt/tty_js.go": "$root/_wasm_overlay/tty_js.go",
    "$bt/signals_js.go": "$root/_wasm_overlay/signals_js.go",
    "$cb/clipboard_js.go": "$root/_wasm_overlay/clipboard_js.go"
  }
}
EOF

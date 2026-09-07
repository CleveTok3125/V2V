//go:build js

package tui

import (
	"errors"
	"io"
)

// Interactive is always false on wasm: prompts arrive from browser
// UI, never from a terminal program.
func Interactive() bool { return false }

// Confirm is unreachable on wasm; the browser form owns confirmation.
func Confirm(string) (bool, error) {
	return false, errors.New("tui: không dùng được trên wasm")
}

// Select is unreachable on wasm; the browser form owns selection.
func Select(string, []string, int) (int, error) {
	return 0, errors.New("tui: không dùng được trên wasm")
}

// SelectPiped is unreachable on wasm; kept for API parity.
func SelectPiped(io.Reader, string, []string, int) (int, error) {
	return 0, errors.New("tui: không dùng được trên wasm")
}

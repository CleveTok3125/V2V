//go:build js

package passprompt

import "errors"

// Interactive is always false on wasm: secrets arrive from the HTML
// password field, never from a terminal program.
func Interactive() bool { return false }

// Password is unreachable on wasm; the browser form owns entry.
func Password(PasswordOpts) (string, error) {
	return "", errors.New("passprompt: không dùng được trên wasm")
}

// Confirm is unreachable on wasm; the browser form owns confirmation.
func Confirm(string) (bool, error) {
	return false, errors.New("passprompt: không dùng được trên wasm")
}

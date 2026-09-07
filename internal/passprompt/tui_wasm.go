//go:build js

package passprompt

import "errors"

// Password is unreachable on wasm; the browser form owns entry.
func Password(PasswordOpts) (string, error) {
	return "", errors.New("passprompt: không dùng được trên wasm")
}

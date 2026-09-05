//go:build !js

package main

import (
	"errors"

	"github.com/atotto/clipboard"
)

// copyToClipboard writes text to the OS clipboard (xclip/xsel/wl-copy on
// Linux, native APIs elsewhere). atotto has no js build, so web uses the
// stub in clipboard_wasm.go instead.
func copyToClipboard(s string) error {
	if err := clipboard.WriteAll(s); err != nil {
		return errors.New("không ghi được clipboard (Linux cần xclip, xsel hoặc wl-clipboard)")
	}
	return nil
}

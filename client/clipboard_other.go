//go:build !js

package main

import (
	"errors"
	"time"

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

// shouldClear decides whether a scheduled clearer may wipe. It fires only
// when the clipboard still holds exactly what was copied, never touching
// newer user content; read errors fail closed.
func shouldClear(current, written string, readErr error) bool {
	return readErr == nil && current == written && current != ""
}

// scheduleClipboardClear wipes the clipboard afterSec seconds later, but
// only if it still holds exactly what was copied — newer user content is
// never touched. afterSec <= 0 disables. Failures (e.g. headless) are
// silent: clearing is hygiene, not correctness.
func scheduleClipboardClear(s string, afterSec int) {
	if afterSec <= 0 {
		return
	}
	written := s
	time.AfterFunc(time.Duration(afterSec)*time.Second, func() {
		cur, err := clipboard.ReadAll()
		if shouldClear(cur, written, err) {
			_ = clipboard.WriteAll("")
		}
	})
}

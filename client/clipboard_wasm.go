//go:build js

package main

import "errors"

// copyToClipboard is unsupported on web: atotto/clipboard has no js
// build. Users select text directly in the terminal instead.
func copyToClipboard(string) error {
	return errors.New("clipboard không hỗ trợ trên bản web — bôi đen để copy thủ công")
}

// scheduleClipboardClear is a no-op on web: nothing was ever written.
func scheduleClipboardClear(string, int) {}

package main

import "testing"

func TestSlashFallbackSend(t *testing.T) {
	// Unknown slash without space: reject, never broadcast.
	for _, s := range []string{"/halp", "/tab1", "/quit1", "/", "/verify1x"} {
		if slashFallbackSend(s) {
			t.Errorf("slashFallbackSend(%q) = send, want reject", s)
		}
	}
	// Slash with space: ordinary chat, sent as-is with the slash kept.
	for _, s := range []string{"/hello world", "/ shout", "/tab 1 extra words here"} {
		if !slashFallbackSend(s) {
			t.Errorf("slashFallbackSend(%q) = reject, want send", s)
		}
	}
	// Non-slash input always sends (guarded elsewhere, not here).
	for _, s := range []string{"hello", "a b", ""} {
		if !slashFallbackSend(s) {
			t.Errorf("slashFallbackSend(%q) = reject, want send", s)
		}
	}
}

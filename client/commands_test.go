package main

import "testing"

func TestIsUnknownSlashCommand(t *testing.T) {
	// Anything starting with "/" that reached the guard (i.e. matched no
	// known command) is rejected, with or without spaces.
	for _, s := range []string{"/halp", "/tab1", "/quit1", "/", "/hello world", "/ shout"} {
		if !isUnknownSlashCommand(s) {
			t.Errorf("isUnknownSlashCommand(%q) = false, want true", s)
		}
	}
	// Non-slash input is ordinary chat.
	for _, s := range []string{"hello", "a b", "", "```code```"} {
		if isUnknownSlashCommand(s) {
			t.Errorf("isUnknownSlashCommand(%q) = true, want false", s)
		}
	}
}

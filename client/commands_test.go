package main

import (
	"sync"
	"testing"
)

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

func TestEmitWhoami_ReleasesMutex(t *testing.T) {
	var mu sync.Mutex
	var lines []string
	emit := func(s string) { lines = append(lines, s) }
	// Guest path (empty role): Unlock must still run here.
	emitWhoami(&mu, emit, "Alice", "guest", "", false, "")
	if len(lines) != 1 {
		t.Fatalf("guest: expected 1 line, got %d", len(lines))
	}
	if !mu.TryLock() {
		t.Fatal("guest: displayMu still held after /whoami")
	}
	mu.Unlock()
	// Role path keeps both lines.
	lines = nil
	emitWhoami(&mu, emit, "Bob", "key", "admin", true, "[A] ")
	if len(lines) != 2 {
		t.Fatalf("role: expected 2 lines, got %d", len(lines))
	}
	if !mu.TryLock() {
		t.Fatal("role: displayMu still held after /whoami")
	}
	mu.Unlock()
}

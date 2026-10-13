package main

import (
	"strings"
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
	// Role path keeps both rows, as one entry.
	lines = nil
	emitWhoami(&mu, emit, "Bob", "key", "admin", true, "[A] ")
	if len(lines) != 1 {
		t.Fatalf("role: expected 1 entry, got %d", len(lines))
	}
	if !strings.Contains(lines[0], "Bob") || !strings.Contains(lines[0], "Role: admin") {
		t.Fatalf("role: both rows must ride the entry: %q", lines[0])
	}
	if !mu.TryLock() {
		t.Fatal("role: displayMu still held after /whoami")
	}
	mu.Unlock()
}

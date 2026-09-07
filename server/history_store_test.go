package main

import (
	"path/filepath"
	"testing"
	"time"
)

// A logger whose file can never open must degrade to stdout-only,
// never nil-deref.
func TestRotatingLogger_NilFileNoPanic(t *testing.T) {
	rl := &RotatingLogger{Filename: filepath.Join(t.TempDir(), "no-such-dir", "v2v.log"), MaxSize: 1}
	if _, err := rl.Write([]byte("hello\n")); err != nil {
		t.Fatalf("degraded write failed: %v", err)
	}
	if _, err := rl.Write([]byte("world\n")); err != nil {
		t.Fatalf("second degraded write failed: %v", err)
	}
}

// Enqueue-after-close and double close must not panic.
func TestHistoryStore_CloseThenEnqueueNoPanic(t *testing.T) {
	s, err := NewHistoryStore(filepath.Join(t.TempDir(), "history.jsonl"), 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s.EnqueueWire(WireMessage{Type: "chat", Text: "late"}, time.Now())
	if err := s.Close(); err != nil {
		t.Fatalf("double close failed: %v", err)
	}
}

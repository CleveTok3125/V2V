package main

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
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

// Corrupt lines and a >64KB line must be skipped/tolerated, never brick
// the load.
func TestLoadRecords_CorruptAndOversized(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "history.jsonl")
	var sb strings.Builder
	sb.WriteString("{not json}\n")
	sb.WriteString(`{"ts":"2024-01-01T00:00:00Z","msg":"good one"}` + "\n")
	sb.WriteString("\n")
	sb.WriteString(`{"ts":"x","wire":{"type":"chat","text":"` + strings.Repeat("A", 200*1024) + `"}}` + "\n")
	sb.WriteString(`{"ts":"y"}` + "\n") // empty record: neither msg nor wire
	if err := os.WriteFile(path, []byte(sb.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	h := &HistoryStore{Filename: path, queue: make(chan historyRecord, 8)}
	recs, err := h.LoadRecords()
	if err != nil {
		t.Fatalf("corrupt/oversized load failed: %v", err)
	}
	if len(recs) != 2 {
		t.Fatalf("want 2 records (good + oversized), got %d", len(recs))
	}
	if recs[0].Message != "good one" {
		t.Fatalf("first record mangled: %+v", recs[0])
	}
	if recs[1].Wire == nil || len(recs[1].Wire.Text) != 200*1024 {
		t.Fatalf("oversized wire record lost")
	}
}

// Concurrent history senders must share one read lock correctly:
// every sender releases exactly the lock it acquired. Runs under -race.
func TestSendChatHistory_NoDoubleUnlock(t *testing.T) {
	testCfg(t)
	s := NewChatServer()
	for i := 0; i < 50; i++ {
		s.appendMessageToHistory("line")
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sess := &ClientSession{Send: make(chan []byte, 1024)}
			s.SendChatHistory(sess)
			for len(sess.Send) > 0 {
				<-sess.Send
			}
		}()
	}
	wg.Wait()
}

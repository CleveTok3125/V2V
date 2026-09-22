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

// History and log files hold plaintext chat: they must land
// owner-only, and their parent dir must not be traversable.
func TestHistoryStore_Permissions(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "history.jsonl")
	s, err := NewHistoryStore(path, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("history perm = %o, want 600", fi.Mode().Perm())
	}
	di, err := os.Stat(filepath.Join(dir, "sub"))
	if err != nil {
		t.Fatal(err)
	}
	if di.Mode().Perm() != 0o700 {
		t.Fatalf("history dir perm = %o, want 700", di.Mode().Perm())
	}
}

func TestRotatingLogger_Permissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v2v.log")
	rl := &RotatingLogger{Filename: path, MaxSize: 1024 * 1024}
	if err := rl.open(); err != nil {
		t.Fatal(err)
	}
	if rl.file != nil {
		defer rl.file.Close()
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("log perm = %o, want 600", fi.Mode().Perm())
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

// A rotate that fails after closing the active file must leave a
// consistent fileless store (no stale handle, zero size) and the store
// must reopen on the next write instead of stalling until restart.
func TestHistoryStore_RotateFailureSelfHeals(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "history.jsonl")
	s, err := NewHistoryStore(path, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	// Occupy the rotation target with a non-empty dir so os.Rename fails
	// deterministically (no permission games).
	blocker := path + ".old"
	if err := os.Mkdir(blocker, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(blocker, "x"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	s.mu.Lock()
	rotErr := s.rotate()
	staleFile := s.file
	staleSize := s.size
	s.mu.Unlock()
	if rotErr == nil {
		t.Fatal("expected rotate to fail while .old is a non-empty dir")
	}
	if staleFile != nil {
		t.Fatal("failed rotate left a stale file handle")
	}
	if staleSize != 0 {
		t.Fatalf("failed rotate left size=%d, want 0", staleSize)
	}

	if err := os.RemoveAll(blocker); err != nil {
		t.Fatal(err)
	}
	if err := s.writeRecord(historyRecord{Message: "after-failure"}); err != nil {
		t.Fatalf("write after failed rotate: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "after-failure") {
		t.Fatalf("self-heal write missing from %s: %q", path, data)
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
		s.Chain.appendMessageToHistory("line")
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sess := &ClientSession{Send: make(chan []byte, 1024)}
			s.Chain.SendChatHistory(sess)
			for len(sess.Send) > 0 {
				<-sess.Send
			}
		}()
	}
	wg.Wait()
}

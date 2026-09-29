package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestHistorySeqMonotonic: notices and chained lines share one monotonic
// history cursor, assigned in storage order.
func TestHistorySeqMonotonic(t *testing.T) {
	testCfg(t)
	s := NewChatServer()
	s.Hub.BroadcastNotice("day 1", "date", nil)
	w, _ := s.Chain.linkAndStore(WireMessage{Type: "chat", Time: "12:00", DisplayName: "A", Text: "hi"}, "")
	s.Hub.BroadcastNotice("A joined", "join", nil)

	if w.Seq != 2 {
		t.Fatalf("chained seq = %d, want 2", w.Seq)
	}
	var got []uint64
	s.Chain.Mu.RLock()
	for _, line := range s.Chain.History {
		var wm WireMessage
		if err := json.Unmarshal([]byte(line), &wm); err != nil {
			s.Chain.Mu.RUnlock()
			t.Fatalf("ram line not a wire: %q", line)
		}
		got = append(got, wm.Seq)
	}
	s.Chain.Mu.RUnlock()
	if fmt.Sprint(got) != "[1 2 3]" {
		t.Fatalf("seq order = %v, want [1 2 3]", got)
	}
}

// TestHistorySeqPersists: seqs written to disk reload unchanged, and the
// next assignment continues past them.
func TestHistorySeqPersists(t *testing.T) {
	testCfg(t)
	path := filepath.Join(t.TempDir(), "history.jsonl")

	s1 := NewChatServer()
	if err := s1.InitHistoryStore(path, 50); err != nil {
		t.Fatal(err)
	}
	s1.Hub.BroadcastNotice("day 1", "date", nil)
	s1.Chain.linkAndStore(WireMessage{Type: "chat", Time: "12:00", DisplayName: "A", Text: "hi"}, "")
	s1.Hub.BroadcastNotice("A joined", "join", nil)
	// Close does not drain the async write queue; wait until the records
	// land on disk before closing.
	deadline := time.Now().Add(3 * time.Second)
	for {
		recs, err := s1.Chain.Store.LoadRecords()
		if err == nil && len(recs) >= 3 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("store did not flush records")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := s1.Chain.Store.Close(); err != nil {
		t.Fatal(err)
	}

	s2 := NewChatServer()
	if err := s2.InitHistoryStore(path, 50); err != nil {
		t.Fatal(err)
	}
	var got []uint64
	for _, line := range s2.Chain.History {
		var wm WireMessage
		if err := json.Unmarshal([]byte(line), &wm); err != nil {
			t.Fatalf("reloaded line not a wire: %q", line)
		}
		got = append(got, wm.Seq)
	}
	if fmt.Sprint(got) != "[1 2 3]" {
		t.Fatalf("reloaded seqs = %v, want [1 2 3]", got)
	}
	if s2.Chain.seq != 3 {
		t.Fatalf("resumed seq = %d, want 3", s2.Chain.seq)
	}
}

// seedSeqLines stores n notices, giving them seqs 1..n.
func seedSeqLines(s *ChatServer, n int) {
	for i := 1; i <= n; i++ {
		s.Hub.BroadcastNotice(fmt.Sprintf("n%d", i), "date", nil)
	}
}

func seqTexts(lines []string) []string {
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		var wm WireMessage
		_ = json.Unmarshal([]byte(l), &wm)
		out = append(out, wm.Text)
	}
	return out
}

func TestCollectSeqPaging(t *testing.T) {
	testCfg(t)
	s := NewChatServer()
	seedSeqLines(s, 5)

	after, more, next := s.Chain.collectAfterSeq(0, 2)
	if fmt.Sprint(seqTexts(after)) != "[n1 n2]" || !more || next != 2 {
		t.Fatalf("after 0/2 = %v more=%v next=%d", seqTexts(after), more, next)
	}
	after, more, next = s.Chain.collectAfterSeq(2, 10)
	if fmt.Sprint(seqTexts(after)) != "[n3 n4 n5]" || more || next != 0 {
		t.Fatalf("after 2/10 = %v more=%v next=%d", seqTexts(after), more, next)
	}

	before, more, next := s.Chain.collectBeforeSeq(0, 2)
	if fmt.Sprint(seqTexts(before)) != "[n5 n4]" || !more || next != 4 {
		t.Fatalf("before 0/2 = %v more=%v next=%d", seqTexts(before), more, next)
	}
	before, more, next = s.Chain.collectBeforeSeq(4, 10)
	if fmt.Sprint(seqTexts(before)) != "[n3 n2 n1]" || more || next != 0 {
		t.Fatalf("before 4/10 = %v more=%v next=%d", seqTexts(before), more, next)
	}
}

// drainSeq runs serveHistorySeq and returns the trailer.
func drainSeq(t *testing.T, s *ChatServer, after, before *uint64, limit int) HistorySync {
	t.Helper()
	sess := &ClientSession{Send: make(chan []byte, 4096), DisplayName: "T#0000", Perms: GetDefaultPermission()}
	done := make(chan struct{})
	go func() {
		s.serveHistorySeq(sess, after, before, limit)
		close(done)
	}()
	timeout := time.After(10 * time.Second)
	for {
		select {
		case msg := <-sess.Send:
			var hs HistorySync
			if err := json.Unmarshal(msg, &hs); err == nil && hs.Type == "history_sync" {
				<-done
				return hs
			}
		case <-timeout:
			t.Fatal("seq page stalled before trailer")
		}
	}
}

func TestServeHistorySeq(t *testing.T) {
	testCfg(t)
	s := NewChatServer()
	seedSeqLines(s, 5)

	zero := uint64(0)
	hs := drainSeq(t, s, &zero, nil, 2)
	if hs.Direction != "after" || hs.FirstSeq != 1 || hs.LastSeq != 2 || hs.NextSeq != 2 || !hs.More {
		t.Fatalf("after trailer = %+v", hs)
	}
	hs = drainSeq(t, s, nil, &zero, 2)
	if hs.Direction != "before" || hs.FirstSeq != 5 || hs.LastSeq != 4 || hs.NextSeq != 4 || !hs.More {
		t.Fatalf("before trailer = %+v", hs)
	}
}

// TestReplayBatchedFrames: a replay coalesces content lines into
// multi-line frames while keeping the trailer separate.
func TestReplayBatchedFrames(t *testing.T) {
	testCfg(t)
	s := NewChatServer()
	for i := 1; i <= 100; i++ {
		s.Chain.appendMessageToHistory(tagLine(uint64(i), "chat", "", "line"))
	}
	sess := &ClientSession{Send: make(chan []byte, 4096), DisplayName: "T#0000", Perms: GetDefaultPermission()}
	done := make(chan struct{})
	go func() {
		s.Chain.SendChatHistory(sess)
		close(done)
	}()
	frames, lines := 0, 0
	timeout := time.After(10 * time.Second)
	for {
		select {
		case msg := <-sess.Send:
			frames++
			lines += len(replayFrameLines(msg))
			var hs HistorySync
			if json.Unmarshal(msg, &hs) == nil && hs.Type == "history_sync" {
				<-done
				if hs.Sent != 100 {
					t.Fatalf("trailer sent = %d, want 100", hs.Sent)
				}
				if frames >= lines {
					t.Fatalf("expected batched frames: frames=%d lines=%d", frames, lines)
				}
				return
			}
		case <-timeout:
			t.Fatal("replay stalled")
		}
	}
}

// TestHistorySeqBackfill: records written before seq existed get one in
// load order, and an explicit seq is preserved.
func TestHistorySeqBackfill(t *testing.T) {
	testCfg(t)
	path := filepath.Join(t.TempDir(), "history.jsonl")
	lines := []string{
		`{"ts":"2026-01-01T00:00:00Z","wire":{"type":"system","text":"day"}}`,
		`{"ts":"2026-01-01T00:01:00Z","wire":{"type":"chat","text":"hi","chain_height":1}}`,
		`{"seq":9,"ts":"2026-01-01T00:02:00Z","wire":{"type":"system","text":"day2","seq":9}}`,
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := NewChatServer()
	if err := s.InitHistoryStore(path, 50); err != nil {
		t.Fatal(err)
	}
	var got []uint64
	for _, line := range s.Chain.History {
		var wm WireMessage
		if err := json.Unmarshal([]byte(line), &wm); err != nil {
			t.Fatalf("line not a wire: %q", line)
		}
		got = append(got, wm.Seq)
	}
	if fmt.Sprint(got) != "[1 2 9]" {
		t.Fatalf("backfilled seqs = %v, want [1 2 9]", got)
	}
	if s.Chain.seq != 9 {
		t.Fatalf("resumed seq = %d, want 9", s.Chain.seq)
	}
}

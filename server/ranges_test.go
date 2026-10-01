package main

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/CleveTok3125/V2V/internal/wire"
)

func seedHeights(s *ChatServer, heights ...uint64) {
	for _, h := range heights {
		s.Chain.appendMessageToHistory(tagLine(h, "chat", "", "line"))
	}
}

func TestCollectRanges(t *testing.T) {
	testCfg(t)
	s := NewChatServer()
	seedHeights(s, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10)

	got := s.Chain.collectRanges([]HeightRange{{From: 2, To: 4}, {From: 8, To: 9}}, 100)
	var hs []uint64
	for _, l := range got {
		var w WireMessage
		if err := json.Unmarshal([]byte(l), &w); err != nil {
			t.Fatalf("line not a wire: %q", l)
		}
		hs = append(hs, w.ChainHeight)
	}
	if fmt.Sprint(hs) != "[2 3 4 8 9]" {
		t.Fatalf("ranges = %v, want [2 3 4 8 9]", hs)
	}

	if got := s.Chain.collectRanges([]HeightRange{{From: 1, To: 10}}, 3); len(got) != 3 {
		t.Fatalf("limit = %d lines, want 3", len(got))
	}
	if got := s.Chain.collectRanges([]HeightRange{{From: 0, To: 3}, {From: 5, To: 2}}, 100); len(got) != 0 {
		t.Fatalf("invalid ranges must yield nothing, got %d", len(got))
	}
}

// drainRanges runs serveHistoryRanges and returns its content lines and
// trailer.
func drainRanges(t *testing.T, s *ChatServer, ranges []HeightRange, limit int) (contents []string, trailer HistorySync) {
	t.Helper()
	sess := &ClientSession{Send: make(chan []byte, 4096), DisplayName: "T#0000", Perms: GetDefaultPermission()}
	done := make(chan struct{})
	go func() {
		s.serveHistoryRanges(sess, ranges, limit)
		close(done)
	}()
	timeout := time.After(10 * time.Second)
	for {
		select {
		case msg := <-sess.Send:
			for _, line := range replayFrameLines(msg) {
				var hs HistorySync
				if err := json.Unmarshal([]byte(line), &hs); err == nil && hs.Type == "history_sync" {
					<-done
					return contents, hs
				}
				if wire.HasTag(markerTags(line), wire.TagHistory) {
					continue
				}
				contents = append(contents, line)
			}
		case <-timeout:
			t.Fatal("range refill stalled before trailer")
		}
	}
}

func TestServeHistoryRanges(t *testing.T) {
	testCfg(t)
	s := NewChatServer()
	seedHeights(s, 1, 2, 3, 4, 5)
	contents, trailer := drainRanges(t, s, []HeightRange{{From: 2, To: 3}}, 100)
	if len(contents) != 2 {
		t.Fatalf("refill = %d lines, want 2: %q", len(contents), contents)
	}
	if trailer.Sent != 2 || trailer.MinHeight != 2 || trailer.MaxHeight != 3 {
		t.Fatalf("refill trailer = %+v", trailer)
	}
}

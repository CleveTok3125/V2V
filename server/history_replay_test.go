package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

// tagLine builds one stored history line with chain fields and kind.
func tagLine(height uint64, typ, kind, text string) string {
	raw, _ := json.Marshal(WireMessage{
		Type: typ, Time: "12:00", SysKind: kind, Text: text,
		ChainHash:   fmt.Sprintf("%064x", height),
		ChainHeight: height, ChainVer: 2,
	})
	return string(raw)
}

// drainReplay runs SendChatHistory and splits the stream into content
// lines, footer and trailer.
func drainReplay(t *testing.T, s *ChatServer, wantJoins bool) (contents []string, footer string, trailer HistorySync) {
	t.Helper()
	sess := &ClientSession{Send: make(chan []byte, 4096), DisplayName: "T#0000", Perms: GetDefaultPermission(), WantJoins: wantJoins}
	done := make(chan struct{})
	go func() {
		s.SendChatHistory(sess)
		close(done)
	}()
	timeout := time.After(10 * time.Second)
	for {
		select {
		case msg := <-sess.Send:
			var hs HistorySync
			if err := json.Unmarshal(msg, &hs); err == nil && hs.Type == "history_sync" {
				trailer = hs
				<-done
				return contents, footer, trailer
			}
			text := string(msg)
			if strings.Contains(text, "Kết thúc lịch sử") {
				footer = text
				continue
			}
			if strings.Contains(text, "Lịch sử chat gần đây") {
				continue
			}
			contents = append(contents, text)
		case <-timeout:
			t.Fatal("replay stream stalled before trailer")
		}
	}
}

func seedReplayHistory(s *ChatServer) {
	lines := []string{
		tagLine(1, "system", "date", "day 1"),
		tagLine(2, "system", "join", "A joined"),
		tagLine(3, "chat", "", "hello"),
		tagLine(4, "system", "leave", "A left"),
		tagLine(5, "chat", "", "world"),
	}
	for _, l := range lines {
		s.appendMessageToHistory(l)
	}
}

// Filtered replay carries chats, dates and untagged lines only; joins
// land in the trailer omission set with exact window bounds.
func TestReplay_Filtered(t *testing.T) {
	testCfg(t)
	s := NewChatServer()
	seedReplayHistory(s)
	contents, footer, trailer := drainReplay(t, s, false)
	if len(contents) != 3 {
		t.Fatalf("filtered replay = %d lines, want 3 (date+2 chats): %q", len(contents), contents)
	}
	if !strings.Contains(footer, "(3/5)") {
		t.Fatalf("footer missing counts: %q", footer)
	}
	if trailer.MinHeight != 1 || trailer.MaxHeight != 5 || trailer.Sent != 3 || trailer.Total != 5 {
		t.Fatalf("trailer bounds/counts wrong: %+v", trailer)
	}
	if len(trailer.OmittedHashes) != 2 || trailer.Truncated {
		t.Fatalf("omission set wrong: %+v", trailer)
	}
}

// Requested replay carries everything, omission set stays empty.
func TestReplay_WithJoins(t *testing.T) {
	testCfg(t)
	s := NewChatServer()
	seedReplayHistory(s)
	contents, footer, trailer := drainReplay(t, s, true)
	if len(contents) != 5 {
		t.Fatalf("full replay = %d lines, want 5: %q", len(contents), contents)
	}
	if !strings.Contains(footer, "(5/5)") {
		t.Fatalf("footer missing counts: %q", footer)
	}
	if len(trailer.OmittedHashes) != 0 {
		t.Fatalf("omission set must be empty: %+v", trailer)
	}
}

// The live 142-line shape: sparse chats buried in join/leave noise.
// Filtered replay must yield exactly the meaningful lines with exact
// trailer accounting.
func TestReplay_LiveShape142(t *testing.T) {
	testCfg(t)
	s := NewChatServer()
	var chats, dates, joins int
	for h := uint64(1); h <= 142; h++ {
		var line string
		switch {
		case h%30 == 1:
			line = tagLine(h, "system", "date", "day marker")
			dates++
		case h%10 == 0:
			line = tagLine(h, "chat", "", "message")
			chats++
		case h%2 == 0:
			line = tagLine(h, "system", "join", "visitor joined")
			joins++
		default:
			line = tagLine(h, "system", "leave", "visitor left")
			joins++
		}
		s.appendMessageToHistory(line)
	}
	contents, footer, trailer := drainReplay(t, s, false)
	want := chats + dates
	if len(contents) != want {
		t.Fatalf("filtered replay = %d lines, want %d (chats+dates)", len(contents), want)
	}
	if !strings.Contains(footer, fmt.Sprintf("(%d/142)", want)) {
		t.Fatalf("footer missing counts: %q", footer)
	}
	if trailer.MinHeight != 1 || trailer.MaxHeight != 142 || trailer.Sent != want || trailer.Total != 142 {
		t.Fatalf("trailer wrong: %+v", trailer)
	}
	if len(trailer.OmittedHashes) != joins {
		t.Fatalf("omitted = %d, want %d joins", len(trailer.OmittedHashes), joins)
	}
}

package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

// tagLine builds one stored history line with chain fields and kind
// (chat, audit, or pre-split system records).
func tagLine(height uint64, typ, kind, text string) string {
	raw, _ := json.Marshal(WireMessage{
		Type: typ, Time: "12:00", SysKind: kind, Text: text,
		ChainHash:   fmt.Sprintf("%064x", height),
		ChainHeight: height, ChainVer: 2,
	})
	return string(raw)
}

// noticeLine builds a new-style unchained notification: tagged kind,
// no chain fields, no height. The chain never advances over these.
func noticeLine(kind, text string) string {
	raw, _ := json.Marshal(WireMessage{
		Type: "system", Time: "12:00", SysKind: kind, Text: text,
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
		noticeLine("date", "day 1"),
		noticeLine("join", "A joined"),
		tagLine(3, "chat", "", "hello"),
		noticeLine("leave", "A left"),
		tagLine(5, "chat", "", "world"),
		// Pre-split chained join: no tag, always replayed.
		tagLine(6, "system", "", "old join"),
	}
	for _, l := range lines {
		s.appendMessageToHistory(l)
	}
}

// Filtered replay carries chats, dates, audits and untagged lines;
// tagged joins/leaves are skipped without touching trailer bounds.
func TestReplay_Filtered(t *testing.T) {
	testCfg(t)
	s := NewChatServer()
	seedReplayHistory(s)
	contents, footer, trailer := drainReplay(t, s, false)
	if len(contents) != 4 {
		t.Fatalf("filtered replay = %d lines, want 4 (date+2 chats+old join): %q", len(contents), contents)
	}
	if !strings.Contains(footer, "(4/6)") {
		t.Fatalf("footer missing counts: %q", footer)
	}
	if trailer.MinHeight != 3 || trailer.MaxHeight != 6 || trailer.Sent != 4 || trailer.Total != 6 {
		t.Fatalf("trailer bounds/counts wrong: %+v", trailer)
	}
}

// Requested replay carries everything, including tagged joins.
func TestReplay_WithJoins(t *testing.T) {
	testCfg(t)
	s := NewChatServer()
	seedReplayHistory(s)
	contents, footer, trailer := drainReplay(t, s, true)
	if len(contents) != 6 {
		t.Fatalf("full replay = %d lines, want 6: %q", len(contents), contents)
	}
	if !strings.Contains(footer, "(6/6)") {
		t.Fatalf("footer missing counts: %q", footer)
	}
	if trailer.MinHeight != 3 || trailer.MaxHeight != 6 {
		t.Fatalf("trailer bounds wrong: %+v", trailer)
	}
}

// The live 142-line shape: sparse chats buried in join/leave noise.
// New-style notices carry no chain fields; filtered replay yields
// exactly the meaningful lines with exact trailer accounting.
func TestReplay_LiveShape142(t *testing.T) {
	testCfg(t)
	s := NewChatServer()
	var chats, dates int
	var firstChat, lastChat uint64
	var height uint64
	for i := 1; i <= 142; i++ {
		switch {
		case i%30 == 1:
			s.appendMessageToHistory(noticeLine("date", "day marker"))
			dates++
		case i%10 == 0:
			height++
			if firstChat == 0 {
				firstChat = height
			}
			lastChat = height
			s.appendMessageToHistory(tagLine(height, "chat", "", "message"))
			chats++
		case i%2 == 0:
			s.appendMessageToHistory(noticeLine("join", "visitor joined"))
		default:
			s.appendMessageToHistory(noticeLine("leave", "visitor left"))
		}
	}
	contents, footer, trailer := drainReplay(t, s, false)
	want := chats + dates
	if len(contents) != want {
		t.Fatalf("filtered replay = %d lines, want %d (chats+dates)", len(contents), want)
	}
	if !strings.Contains(footer, fmt.Sprintf("(%d/142)", want)) {
		t.Fatalf("footer missing counts: %q", footer)
	}
	if trailer.MinHeight != firstChat || trailer.MaxHeight != lastChat || trailer.Sent != want || trailer.Total != 142 {
		t.Fatalf("trailer wrong: %+v (want min=%d max=%d)", trailer, firstChat, lastChat)
	}
}

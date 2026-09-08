package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/CleveTok3125/V2V/internal/config"
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

// TestSendChatHistory_NeverBlocks: a dead peer (WritePump gone, channel
// full, no reader) must not wedge the replay, which runs under
// BroadcastMu — blocking here would stall every broadcast.
func TestSendChatHistory_NeverBlocks(t *testing.T) {
	testCfg(t)
	s := NewChatServer()
	for i := 0; i < 50; i++ {
		s.appendMessageToHistory(tagLine(uint64(i+1), "chat", "", "dead peer line"))
	}
	sess := &ClientSession{Send: make(chan []byte), DisplayName: "Ghost#0000", Perms: GetDefaultPermission()}
	done := make(chan struct{})
	go func() {
		s.SendChatHistory(sess)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("SendChatHistory blocked on a dead peer")
	}
}

// TestRegister_HoldsBroadcastMu: registerClient must hold BroadcastMu for
// the whole replay, otherwise concurrent live chats interleave between
// replay lines and the trailer and poison the fork window.
func TestRegister_HoldsBroadcastMu(t *testing.T) {
	testCfg(t)
	cfg := config.DefaultDynamic()
	cfg.MaxHistorySend = 50000
	Cfg.Dynamic.Store(cfg)
	s := NewChatServer()
	for i := 0; i < 50000; i++ {
		s.appendMessageToHistory(tagLine(uint64(i+1), "chat", "", "bulk line"))
	}
	sess := &ClientSession{Send: make(chan []byte, 1 << 20), DisplayName: "New#0000", Perms: GetDefaultPermission()}
	regDone := make(chan struct{})
	go func() {
		s.registerClient(sess, "127.0.0.1")
		close(regDone)
	}()
	lockedObserved := false
	for {
		select {
		case <-regDone:
			goto checked
		default:
		}
		if s.BroadcastMu.TryLock() {
			s.BroadcastMu.Unlock()
		} else {
			lockedObserved = true
		}
	}
checked:
	if !lockedObserved {
		t.Fatal("registerClient never held BroadcastMu during replay")
	}
	close(sess.Send)
	n := 0
	for range sess.Send {
		n++
	}
	if n == 0 {
		t.Fatal("replay delivered nothing")
	}
}

// TestRegister_NoLiveInterleave: live chats racing a replay must land
// strictly after the trailer. The broadcaster starts only after the
// replay header proves the lock is held.
func TestRegister_NoLiveInterleave(t *testing.T) {
	testCfg(t)
	s := NewChatServer()
	const replayLines = 300
	for i := 0; i < replayLines; i++ {
		s.appendMessageToHistory(tagLine(uint64(i+1), "chat", "", "replay line"))
	}
	sess := &ClientSession{Conn: nil, Send: make(chan []byte, 16384), DisplayName: "New#0000", Perms: GetDefaultPermission()}
	regDone := make(chan struct{})
	go func() {
		s.registerClient(sess, "127.0.0.1")
		close(regDone)
	}()
	// Wait for the replay header: proves registerClient is inside the
	// locked region before any live broadcast fires.
	header := <-sess.Send
	if !strings.Contains(string(header), "Lịch sử chat gần đây") {
		t.Fatalf("first stream item is not the replay header: %q", header)
	}
	liveDone := make(chan struct{})
	go func() {
		defer close(liveDone)
		for i := 0; i < 50; i++ {
			s.BroadcastWire(WireMessage{Type: "chat", Text: "live line", DisplayName: "Other#1111"}, nil)
		}
	}()
	<-regDone
	<-liveDone
	close(sess.Send)
	stream := []string{string(header)}
	for msg := range sess.Send {
		stream = append(stream, string(msg))
	}
	trailerIdx, firstLiveIdx := -1, -1
	for i, m := range stream {
		var hs HistorySync
		if err := json.Unmarshal([]byte(m), &hs); err == nil && hs.Type == "history_sync" {
			trailerIdx = i
		}
		if strings.Contains(m, "live line") && firstLiveIdx < 0 {
			firstLiveIdx = i
		}
	}
	if trailerIdx < 0 {
		t.Fatal("trailer missing from stream")
	}
	if firstLiveIdx >= 0 && firstLiveIdx < trailerIdx {
		t.Fatalf("live message interleaved into replay at %d (trailer at %d)", firstLiveIdx, trailerIdx)
	}
}

// BroadcastAudit is chained, verifiable, delivered live, and always
// replayed: moderation evidence must survive filtering.
func TestAudit_LiveAndReplay(t *testing.T) {
	testCfg(t)
	s := NewChatServer()
	peer := &ClientSession{Send: make(chan []byte, 64), DisplayName: "P#0000", Perms: GetDefaultPermission()}
	s.ClientsMu.Lock()
	s.Clients[peerConn()] = peer
	s.ClientsMu.Unlock()
	s.BroadcastAudit("moderation note", nil)
	select {
	case m := <-peer.Send:
		var w WireMessage
		if err := json.Unmarshal(m, &w); err != nil || w.SysKind != "audit" || w.ChainHeight != 1 {
			t.Fatalf("live audit mangled: %q", m)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("audit not delivered live")
	}
	if s.chainHeight != 1 {
		t.Fatalf("audit must advance the chain, height=%d", s.chainHeight)
	}
	s.appendMessageToHistory(tagLine(2, "chat", "", "after audit"))
	filtered, _, trailer := drainReplay(t, s, false)
	found := false
	for _, m := range filtered {
		if strings.Contains(m, "moderation note") {
			found = true
		}
	}
	if !found {
		t.Fatalf("audit missing from filtered replay: %q", filtered)
	}
	if trailer.MinHeight != 1 || trailer.MaxHeight != 2 {
		t.Fatalf("trailer bounds wrong with audit: %+v", trailer)
	}
}

func peerConn() *websocket.Conn { return &websocket.Conn{} }

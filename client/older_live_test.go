package main

import (
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/CleveTok3125/V2V/lib/chain"
	"github.com/CleveTok3125/V2V/lib/config"
	"github.com/CleveTok3125/V2V/lib/wire"
)

func olderLiveTestWire(height, seq uint64, text string) WireMessage {
	var prev [32]byte
	prev[0] = 0x11
	w := WireMessage{Type: "chat", Time: "12:00", DisplayName: "A", Text: text, ChainHeight: height, ChainVer: 2, Seq: seq}
	w.ChainPrev = hex.EncodeToString(prev[:])
	h := chain.Hash(prev, height, 0, 0, "chat", "12:00", "A", text, "")
	w.ChainHash = hex.EncodeToString(h[:])
	return w
}

func TestIsOlderHistoryWire(t *testing.T) {
	old := ClientCfg
	ClientCfg = config.DefaultClientConfig()
	defer func() { ClientCfg = old }()
	sess := NewSession()
	sess.Display.TabChat = newTabBuffer(100, 100000)
	sess.Display.TabSys = newTabBuffer(100, 100000)

	sess.Chain.OlderHeightBound = 0
	sess.Chain.OlderSeqBound = 0
	if !sess.isOlderHistoryWire(WireMessage{Type: "system", Seq: 0}) {
		t.Fatal("marker window with no bounds must splice everything")
	}

	sess.Chain.OlderHeightBound = 5
	sess.Chain.OlderSeqBound = 10
	if !sess.isOlderHistoryWire(olderLiveTestWire(3, 8, "old")) {
		t.Fatal("chained height below bound must splice")
	}
	if sess.isOlderHistoryWire(olderLiveTestWire(7, 12, "live")) {
		t.Fatal("chained height at/above bound must append")
	}
	if !sess.isOlderHistoryWire(WireMessage{Type: "system", Text: "old notice", Seq: 9}) {
		t.Fatal("notice seq below bound must splice")
	}
	if sess.isOlderHistoryWire(WireMessage{Type: "system", Text: "live", Seq: 12}) {
		t.Fatal("notice seq above bound must append")
	}
	if sess.isOlderHistoryWire(WireMessage{Type: "system", Text: "direct"}) {
		t.Fatal("seq-less direct system message must append, never splice")
	}
}

func TestIsLoadHistoryWire(t *testing.T) {
	sess := NewSession()
	sess.Chain.LoadStartSeq = 9
	sess.Chain.LoadMaxSeq = 10
	if !sess.isLoadHistoryWire(WireMessage{Type: "chat", Seq: 9}) {
		t.Fatal("seq in window must be history")
	}
	if !sess.isLoadHistoryWire(WireMessage{Type: "chat", Seq: 10}) {
		t.Fatal("window end must be history")
	}
	if sess.isLoadHistoryWire(WireMessage{Type: "chat", Seq: 11}) {
		t.Fatal("seq past window must be held as live")
	}
	if sess.isLoadHistoryWire(WireMessage{Type: "system", Text: "direct"}) {
		t.Fatal("seq-less direct message must be held as live")
	}
	if !sess.isLoadHistoryWire(WireMessage{Type: "chat", ChainHeight: 3}) {
		t.Fatal("legacy chained line without seq must be history")
	}
}

func TestWireIndexMinSeq(t *testing.T) {
	x := newWireIndex(16)
	if x.minSeq() != 0 {
		t.Fatal("empty index must report 0")
	}
	x.put(WireMessage{ChainHeight: 6, Seq: 11})
	x.put(WireMessage{ChainHeight: 5, Seq: 10})
	if got := x.minSeq(); got != 10 {
		t.Fatalf("minSeq = %d, want 10", got)
	}
	y := newWireIndex(16)
	y.put(WireMessage{ChainHeight: 5})
	if y.minSeq() != 0 {
		t.Fatal("all-legacy index must report 0")
	}
}

func TestOlderLiveUnicastAppends(t *testing.T) {
	old := ClientCfg
	ClientCfg = config.DefaultClientConfig()
	defer func() { ClientCfg = old }()
	conn := &captureConn{frames: make(chan []byte, 16)}
	sess := recoverTestSession(t, 9, conn)
	sess.Display.ActiveTab = TabChat

	sess.Display.DisplayMu.Lock()
	sess.renderChatBlock(olderLiveTestWire(5, 10, "msg"))
	sess.renderChatBlock(olderLiveTestWire(6, 11, "msg"))
	sess.Display.DisplayMu.Unlock()
	if ok, err := sess.requestOlder(50); !ok || err != nil {
		t.Fatalf("requestOlder = %v, %v", ok, err)
	}

	frame := func(v any) []byte {
		raw, err := json.Marshal(v)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		return raw
	}
	historyNotice := WireMessage{Type: "system", Time: "12:00", Tags: wire.WithTags(wire.TagDate), Text: "old notice", SysDate: "2026-01-01", Seq: 9}
	liveDirect := WireMessage{Type: "system", Time: "12:01", Tags: wire.WithTags(wire.TagLimit), Text: "live direct", Seq: 0}
	liveChat := olderLiveTestWire(7, 12, "live chat")
	conn.frames <- frame(historyNotice)
	conn.frames <- frame(liveDirect)
	conn.frames <- frame(liveChat)
	conn.frames <- frame(HistorySync{Type: "history_sync", Sent: 1, Total: 1})

	go sess.runPump()
	deadline := time.Now().Add(3 * time.Second)
	for {
		sess.Display.DisplayMu.Lock()
		older := sess.Chain.InOlder
		n := len(sess.Display.TabChat.lines)
		sess.Display.DisplayMu.Unlock()
		if !older && n >= 5 {
			break
		}
		if time.Now().After(deadline) {
			stopPump(t, sess, func() { close(conn.frames) })
			t.Fatalf("older window never settled (inOlder=%v lines=%d)", older, n)
		}
		time.Sleep(5 * time.Millisecond)
	}
	stopPump(t, sess, func() { close(conn.frames) })

	sess.Display.DisplayMu.Lock()
	defer sess.Display.DisplayMu.Unlock()
	var order []string
	for _, e := range sess.Display.TabChat.lines {
		order = append(order, e.text())
	}
	joined := strings.Join(order, "\x00")
	oldIdx := strings.Index(joined, "old notice")
	liveIdx := strings.Index(joined, "live direct")
	chatIdx := strings.Index(joined, "live chat")
	if oldIdx < 0 || liveIdx < 0 || chatIdx < 0 {
		t.Fatalf("missing lines in %q", joined)
	}
	if !(oldIdx < liveIdx && liveIdx < chatIdx) {
		t.Fatalf("history must precede live (old=%d live=%d chat=%d): %q", oldIdx, liveIdx, chatIdx, joined)
	}
	if !strings.HasPrefix(joined, "old notice") && !strings.Contains(order[0], "old notice") {
		t.Fatalf("old history must splice at top, got first %q", order[0])
	}
}

func TestLoadHoldsLive(t *testing.T) {
	old := ClientCfg
	ClientCfg = config.DefaultClientConfig()
	defer func() { ClientCfg = old }()
	sess := NewSession()
	sess.Display.Term = &fakeTerm{}
	sess.Display.ActiveTab = TabChat
	sess.Display.TabSys = newTabBuffer(100, 100000)
	sess.Display.TabChat = newTabBuffer(100, 100000)
	sess.Chain.TipPath = t.TempDir() + "/tip.json"
	sess.Chain.WireIdx = newWireIndex(64)
	sess.Chain.RenderCache = newRenderCache(8)
	sess.Display.ShowJoinLeave = true

	sess.Display.DisplayMu.Lock()
	sess.renderChatBlock(olderLiveTestWire(9, 9, "msg"))
	sess.renderChatBlock(olderLiveTestWire(10, 10, "msg"))
	sess.Chain.Loading = true
	sess.Chain.InSync = true
	sess.Chain.LoadStartSeq = 9
	sess.Chain.LoadMaxSeq = 10
	sess.Chain.LoadHold = []heldLine{
		{msg: WireMessage{Type: "system", Time: "12:01", Tags: wire.WithTags(wire.TagLimit), Text: "live direct"}},
		{msg: olderLiveTestWire(11, 11, "live chat")},
	}
	sess.finishLoadLocked()
	var order []string
	for _, e := range sess.Display.TabChat.lines {
		order = append(order, e.text())
	}
	sess.Display.DisplayMu.Unlock()
	joined := strings.Join(order, "\x00")
	liveIdx := strings.Index(joined, "live direct")
	chatIdx := strings.Index(joined, "live chat")
	if liveIdx < 0 || chatIdx < 0 {
		t.Fatalf("held live missing in %q", joined)
	}
	if !(liveIdx < chatIdx) {
		t.Fatalf("held live must flush in arrival order: %q", joined)
	}
	if strings.Index(joined, "live direct") < strings.Index(joined, "msg") {
		t.Fatalf("held live must land after history: %q", joined)
	}
}

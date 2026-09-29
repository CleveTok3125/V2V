package main

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/CleveTok3125/V2V/internal/chain"
	"github.com/CleveTok3125/V2V/internal/config"
)

// captureConn records outbound frames for gap-recovery assertions.
type captureConn struct {
	mu      sync.Mutex
	written [][]byte
	frames  chan []byte
}

func (c *captureConn) ReadJSON(v any) error { return io.EOF }
func (c *captureConn) WriteJSON(v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	c.mu.Lock()
	c.written = append(c.written, raw)
	c.mu.Unlock()
	return nil
}
func (c *captureConn) ReadMessage() (int, []byte, error) {
	m, ok := <-c.frames
	if !ok {
		return 0, nil, io.EOF
	}
	return 1, m, nil
}
func (c *captureConn) WriteMessage(int, []byte) error { return nil }
func (c *captureConn) SetReadLimit(int64)             {}
func (c *captureConn) Close() error                   { return nil }

func (c *captureConn) lastRequest(t *testing.T) HistoryRequest {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.written) != 1 {
		t.Fatalf("expected exactly 1 request, got %d", len(c.written))
	}
	var req HistoryRequest
	if err := json.Unmarshal(c.written[0], &req); err != nil {
		t.Fatalf("request not a history_request: %v", err)
	}
	return req
}

// recoverTestChain builds n content-valid chained wires continuing the
// anchor hash from start height.
func recoverTestChain(anchor [32]byte, start uint64, n int) []WireMessage {
	var out []WireMessage
	prev := anchor
	for i := 0; i < n; i++ {
		h := start + uint64(i)
		wire := WireMessage{Type: "chat", Time: "12:00", DisplayName: "A", Text: "msg", ChainHeight: h, ChainVer: 2}
		wire.ChainPrev = hex.EncodeToString(prev[:])
		hh := chain.Hash(prev, h, 0, 0, "chat", "12:00", "A", "msg", "")
		wire.ChainHash = hex.EncodeToString(hh[:])
		prev = hh
		out = append(out, wire)
	}
	return out
}

func recoverTestSession(t *testing.T, tip byte, conn *captureConn) *Session {
	t.Helper()
	sess := chainTestSession(t, tip)
	sess.Chain.WireIdx = newWireIndex(64)
	sess.Chain.RenderCache = newRenderCache(8)
	sess.Conn = conn
	return sess
}

func tabSysText(sess *Session) string {
	var sb strings.Builder
	for _, l := range sess.Display.TabSys.lines {
		sb.WriteString(l)
	}
	return sb.String()
}

// waitRecoverSettled waits for the pump to clear the pending refill,
// reading the shared field under DisplayMu like the pump writes it.
func waitRecoverSettled(t *testing.T, sess *Session, deadline time.Duration) {
	t.Helper()
	limit := time.Now().Add(deadline)
	for {
		sess.Display.DisplayMu.Lock()
		pending := sess.Chain.RecoverPending
		sess.Display.DisplayMu.Unlock()
		if pending == nil {
			return
		}
		if time.Now().After(limit) {
			t.Fatal("recovery window never settled")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// setRecoverCfg overrides the client recovery config for a test.
func setRecoverCfg(t *testing.T, retries int, delay string, liveCap int) {
	t.Helper()
	old := ClientCfg
	cfg := config.DefaultClientConfig()
	cfg.History.RecoverRetries = &retries
	cfg.History.RecoverRetryDelay = &delay
	cfg.History.LiveRecoverCap = &liveCap
	ClientCfg = cfg
	t.Cleanup(func() { ClientCfg = old })
}

// A live gap files one range request and seeds the window.
func TestRecoveryRequestOnGap(t *testing.T) {
	conn := &captureConn{frames: make(chan []byte)}
	sess := recoverTestSession(t, 9, conn)
	sess.Display.DisplayMu.Lock()
	ok := sess.maybeRequestRecovery(101, 102, 100, [32]byte{9})
	sess.Display.DisplayMu.Unlock()
	if !ok {
		t.Fatal("live gap must file a refill")
	}
	req := conn.lastRequest(t)
	if len(req.Ranges) != 1 || req.Ranges[0].From != 101 || req.Ranges[0].To != 102 || req.Limit != 2 {
		t.Fatalf("request = %+v", req)
	}
	p := sess.Chain.RecoverPending
	if p == nil || !p.Missing[101] || !p.Missing[102] || p.Known[100] != [32]byte{9} || p.Attempts != 1 {
		t.Fatalf("pending = %+v", p)
	}
}

// A complete refill verifies, renders and confirms.
func TestRecoveryWindowFlow(t *testing.T) {
	conn := &captureConn{frames: make(chan []byte)}
	sess := recoverTestSession(t, 9, conn)
	anchor := [32]byte{9}
	sess.Chain.RecoverPending = &recoverWindow{
		Missing:    map[uint64]bool{101: true, 102: true},
		Known:      map[uint64][32]byte{100: anchor},
		Live:       map[uint64]bool{},
		MaxMissing: 102, Attempts: 1, RequestedAt: time.Now(),
	}
	wires := recoverTestChain(anchor, 101, 2)
	sess.Display.DisplayMu.Lock()
	sess.checkRecoverWire(wires[0])
	sess.checkRecoverWire(wires[1])
	sess.finishRecovery()
	sess.Display.DisplayMu.Unlock()

	if sess.Chain.RecoverPending != nil {
		t.Fatal("complete refill must clear pending")
	}
	for _, h := range []uint64{101, 102} {
		if _, ok := sess.Chain.WireIdx.get(h); !ok {
			t.Fatalf("recovered #%d must index", h)
		}
	}
	if !strings.Contains(tabSysText(sess), "Đã bù 2 tin bị lỡ") {
		t.Fatalf("missing confirmation: %q", tabSysText(sess))
	}
}

// A wire that does not continue its anchor fails; with no retries left
// the loss is reported.
func TestRecoveryMismatchFails(t *testing.T) {
	setRecoverCfg(t, 0, "1ms", 1000)
	conn := &captureConn{frames: make(chan []byte)}
	sess := recoverTestSession(t, 9, conn)
	anchor := [32]byte{9}
	sess.Chain.RecoverPending = &recoverWindow{
		Missing:    map[uint64]bool{101: true},
		Known:      map[uint64][32]byte{100: anchor},
		Live:       map[uint64]bool{},
		MaxMissing: 101, Attempts: 1, RequestedAt: time.Now(),
	}
	bad := chainTestWire(101, 0x11)
	sess.Display.DisplayMu.Lock()
	sess.checkRecoverWire(bad)
	sess.finishRecovery()
	sess.Display.DisplayMu.Unlock()
	if sess.Chain.RecoverPending != nil {
		t.Fatal("failed refill must clear pending")
	}
	if !strings.Contains(tabSysText(sess), "Không bù đủ tin") {
		t.Fatalf("missing failure notice: %q", tabSysText(sess))
	}
	if _, ok := sess.Chain.WireIdx.get(101); ok {
		t.Fatal("failed wire must not index")
	}
}

// An incomplete answer retries the remainder.
func TestRecoveryRetriesIncomplete(t *testing.T) {
	setRecoverCfg(t, 2, "1ms", 1000)
	conn := &captureConn{frames: make(chan []byte)}
	sess := recoverTestSession(t, 9, conn)
	anchor := [32]byte{9}
	sess.Chain.RecoverPending = &recoverWindow{
		Missing:    map[uint64]bool{101: true, 103: true},
		Known:      map[uint64][32]byte{100: anchor, 102: anchor},
		Live:       map[uint64]bool{},
		MaxMissing: 103, RequestedAt: time.Now(),
	}
	wires := recoverTestChain(anchor, 101, 1)
	sess.Display.DisplayMu.Lock()
	if !sess.sendRecoverLocked() {
		sess.Display.DisplayMu.Unlock()
		t.Fatal("first refill must send")
	}
	sess.checkRecoverWire(wires[0])
	sess.finishRecovery()
	sess.Display.DisplayMu.Unlock()

	deadline := time.Now().Add(2 * time.Second)
	for {
		conn.mu.Lock()
		n := len(conn.written)
		conn.mu.Unlock()
		if n >= 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("retry not sent (requests=%d)", n)
		}
		time.Sleep(5 * time.Millisecond)
	}
	conn.mu.Lock()
	raw := conn.written[1]
	conn.mu.Unlock()
	var req HistoryRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		t.Fatalf("retry not a request: %v", err)
	}
	if len(req.Ranges) != 1 || req.Ranges[0].From != 103 || req.Ranges[0].To != 103 {
		t.Fatalf("retry request = %+v", req)
	}
}

// Exhausting retries reports the loss.
func TestRecoveryExhausts(t *testing.T) {
	setRecoverCfg(t, 0, "1ms", 1000)
	conn := &captureConn{frames: make(chan []byte)}
	sess := recoverTestSession(t, 9, conn)
	sess.Chain.RecoverPending = &recoverWindow{
		Missing:    map[uint64]bool{101: true},
		Known:      map[uint64][32]byte{100: {9}},
		Live:       map[uint64]bool{},
		MaxMissing: 101, Attempts: 1, RequestedAt: time.Now(),
	}
	sess.Display.DisplayMu.Lock()
	sess.finishRecovery()
	sess.Display.DisplayMu.Unlock()
	if sess.Chain.RecoverPending != nil {
		t.Fatal("exhausted refill must clear pending")
	}
	if !strings.Contains(tabSysText(sess), "Không bù đủ tin") {
		t.Fatalf("missing notice: %q", tabSysText(sess))
	}
}

// The session live budget refuses a gap past liveRecoverCap.
func TestRecoveryLiveCap(t *testing.T) {
	setRecoverCfg(t, 2, "1ms", 1)
	conn := &captureConn{frames: make(chan []byte)}
	sess := recoverTestSession(t, 9, conn)
	sess.Display.DisplayMu.Lock()
	ok := sess.maybeRequestRecovery(101, 102, 100, [32]byte{9})
	sess.Display.DisplayMu.Unlock()
	if ok || sess.Chain.RecoverPending != nil {
		t.Fatal("gap past the live budget must be refused")
	}
}

// missingRuns coalesces scattered heights into contiguous ranges.
func TestMissingRuns(t *testing.T) {
	runs := missingRuns(map[uint64]bool{1: true, 2: true, 4: true, 7: true, 8: true})
	if fmt.Sprint(runs) != "[{1 2} {4 4} {7 8}]" {
		t.Fatalf("runs = %v", runs)
	}
	if len(missingRuns(nil)) != 0 {
		t.Fatal("no missing heights must yield no runs")
	}
}

// A refill renders the missing heights and skips the received ones.
func TestRecoveryRendersOnlyMissing(t *testing.T) {
	conn := &captureConn{frames: make(chan []byte)}
	sess := recoverTestSession(t, 9, conn)
	anchor := [32]byte{9}
	wires := recoverTestChain(anchor, 100, 5)
	hashes := map[uint64][32]byte{}
	for _, w := range wires {
		h, _ := chain.ParseHex64(w.ChainHash)
		hashes[w.ChainHeight] = h
	}
	sess.Chain.RecoverPending = &recoverWindow{
		Missing:    map[uint64]bool{102: true, 104: true},
		Known:      map[uint64][32]byte{100: hashes[100], 101: hashes[101], 103: hashes[103]},
		Live:       map[uint64]bool{},
		MaxMissing: 104, Attempts: 1, RequestedAt: time.Now(),
	}
	sess.Display.DisplayMu.Lock()
	sess.checkRecoverWire(wires[2]) // 102 missing
	sess.checkRecoverWire(wires[3]) // 103 received
	sess.checkRecoverWire(wires[4]) // 104 missing
	sess.finishRecovery()
	sess.Display.DisplayMu.Unlock()

	got := 0
	for _, l := range sess.Display.TabChat.lines {
		if strings.Contains(l, "msg") {
			got++
		}
	}
	if got != 2 {
		t.Fatalf("rendered %d head lines, want 2 (#102,#104)", got)
	}
	for _, h := range []uint64{102, 104} {
		if _, ok := sess.Chain.WireIdx.get(h); !ok {
			t.Fatalf("#%d must index", h)
		}
	}
	if _, ok := sess.Chain.WireIdx.get(103); ok {
		t.Fatal("received #103 must not be re-rendered")
	}
}

// waitHoldReleased waits for the catch-up hold to end.
func waitHoldReleased(t *testing.T, sess *Session, deadline time.Duration) {
	t.Helper()
	limit := time.Now().Add(deadline)
	for {
		sess.Display.DisplayMu.Lock()
		held := sess.Display.CatchupHold
		sess.Display.DisplayMu.Unlock()
		if !held {
			return
		}
		if time.Now().After(limit) {
			t.Fatal("catch-up hold never released")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestRecoveryMergesInOrder: a refilled block is spliced in after its
// chain predecessor inside a held catch-up, not appended at the end.
func TestRecoveryMergesInOrder(t *testing.T) {
	conn := &captureConn{frames: make(chan []byte)}
	sess := recoverTestSession(t, 9, conn)
	sess.Display.TabSys = newTabBuffer(100, 100000)
	sess.Display.TabChat = newTabBuffer(100, 100000)
	var out bytes.Buffer
	sess.Display.Out = &out
	sess.Display.ActiveTab = TabChat
	sess.Display.ShowMeta = true
	anchor := [32]byte{9}
	wires := recoverTestChain(anchor, 100, 4) // 100..103
	hashes := map[uint64][32]byte{}
	for _, w := range wires {
		h, _ := chain.ParseHex64(w.ChainHash)
		hashes[w.ChainHeight] = h
	}

	// A held join replay of 100,101,103 with 102 missing.
	sess.Display.DisplayMu.Lock()
	sess.Display.CatchupHold = true
	sess.renderChatBlock(wires[0])
	sess.renderChatBlock(wires[1])
	sess.renderChatBlock(wires[3])
	sess.Chain.RecoverPending = &recoverWindow{
		Missing:    map[uint64]bool{102: true},
		Known:      map[uint64][32]byte{101: hashes[101]},
		Live:       map[uint64]bool{},
		MaxMissing: 102, Attempts: 1, RequestedAt: time.Now(),
	}
	sess.checkRecoverWire(wires[2])
	sess.finishRecovery()
	sess.Display.DisplayMu.Unlock()
	sess.flushOutputNow()

	text := out.String()
	prev := -1
	for _, needle := range []string{"#100:", "#101:", "#102:", "#103:"} {
		i := strings.Index(text, needle)
		if i < 0 {
			t.Fatalf("missing %s in output: %q", needle, text)
		}
		if i < prev {
			t.Fatalf("output out of order at %s: %q", needle, text)
		}
		prev = i
	}
}

// TestHandleHistorySyncForkScope: the segment trailer runs no fork
// check; the load's fork check warns on a rewritten height.
func TestHandleHistorySyncForkScope(t *testing.T) {
	seg := recoverTestSession(t, 9, &captureConn{frames: make(chan []byte)})
	seg.Display.TabSys = newTabBuffer(100, 100000)
	seg.Chain.HavePersistedTip = true
	seg.Chain.PersistedTip = [32]byte{0xaa}
	seg.Chain.PersistedHeight = 120
	seg.handleHistorySync(HistorySync{Type: "history_sync", MinHeight: 100, MaxHeight: 130, Sent: 2, Total: 2})
	if strings.Contains(tabSysText(seg), "phân nhánh") {
		t.Fatalf("segment trailer must not warn: %q", tabSysText(seg))
	}

	join := recoverTestSession(t, 9, &captureConn{frames: make(chan []byte)})
	join.Display.TabSys = newTabBuffer(100, 100000)
	join.Chain.HavePersistedTip = true
	join.Chain.PersistedTip = [32]byte{0xaa}
	join.Chain.PersistedHeight = 120
	join.Display.DisplayMu.Lock()
	join.Chain.Loading = true
	join.Chain.SyncHeights[120] = [32]byte{0xbb}
	join.finishLoadLocked()
	join.Display.DisplayMu.Unlock()
	if !strings.Contains(tabSysText(join), "phân nhánh") {
		t.Fatalf("load fork check must warn on a rewritten height: %q", tabSysText(join))
	}
}

// TestGreetingAfterCatchup: the held welcome line prints after the
// loaded history.
func TestGreetingAfterCatchup(t *testing.T) {
	conn := &captureConn{frames: make(chan []byte, 16)}
	sess := recoverTestSession(t, 9, conn)
	sess.Display.TabSys = newTabBuffer(100, 100000)
	sess.Display.TabChat = newTabBuffer(100, 100000)
	var out bytes.Buffer
	sess.Display.Out = &out
	sess.Display.ActiveTab = TabChat
	sess.Display.PendingGreeting = "GREETING\n"
	anchor := [32]byte{9}
	wires := recoverTestChain(anchor, 100, 2)
	frame := func(v any) []byte {
		raw, err := json.Marshal(v)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		return raw
	}
	conn.frames <- frame(HistoryInfo{Type: "history_info", MinSeq: 1, MaxSeq: 2, MinHeight: 100, MaxHeight: 101, Count: 2})
	conn.frames <- []byte("--- Lịch sử chat gần đây ---\n")
	conn.frames <- frame(wires[0])
	conn.frames <- frame(wires[1])
	conn.frames <- []byte("--- Kết thúc lịch sử (2/2) ---\n")
	conn.frames <- frame(HistorySync{Type: "history_sync", Direction: "after", FirstSeq: 1, LastSeq: 2, Sent: 2, Total: 2})

	go sess.runPump()
	waitHoldReleased(t, sess, 3*time.Second)
	close(conn.frames)
	close(sess.Quitting)
	<-sess.PumpDone
	sess.flushOutputNow()

	text := out.String()
	gi := strings.Index(text, "GREETING")
	mi := strings.LastIndex(text, "msg")
	if gi < 0 || mi < 0 || gi < mi {
		t.Fatalf("greeting must follow the history: %q", text)
	}
}

// TestTrackRecoveryWindow: recovery boundaries track their own flag.
func TestTrackRecoveryWindow(t *testing.T) {
	sess := chainTestSession(t, 9)
	sess.Display.DisplayMu.Lock()
	sess.trackReplayWindow("--- Lịch sử bù ---", true)
	if !sess.Chain.InRecover || sess.Chain.InSync || sess.Chain.InOlder {
		sess.Display.DisplayMu.Unlock()
		t.Fatal("recovery header must raise only InRecover")
	}
	sess.trackReplayWindow("--- Kết thúc lịch sử bù (2/2) ---", false)
	if sess.Chain.InRecover {
		sess.Display.DisplayMu.Unlock()
		t.Fatal("recovery footer must clear InRecover")
	}
	sess.Display.DisplayMu.Unlock()
}

// TestLoadPagesUntilMaxSeq: the client pages ascending until it reaches
// the announced window end, sending after_seq = next_seq each time.
func TestLoadPagesUntilMaxSeq(t *testing.T) {
	conn := &captureConn{frames: make(chan []byte, 16)}
	sess := recoverTestSession(t, 9, conn)
	sess.Display.TabSys = newTabBuffer(100, 100000)
	sess.Display.TabChat = newTabBuffer(100, 100000)
	sess.Display.Out = io.Discard
	sess.Display.ActiveTab = TabChat
	anchor := [32]byte{9}
	wires := recoverTestChain(anchor, 1, 3) // heights 1..3
	frame := func(v any) []byte {
		raw, err := json.Marshal(v)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		return raw
	}
	conn.frames <- frame(HistoryInfo{Type: "history_info", MinSeq: 1, MaxSeq: 3, MinHeight: 1, MaxHeight: 3, Count: 3})
	conn.frames <- []byte("--- Lịch sử chat gần đây ---\n")
	conn.frames <- frame(wires[0])
	conn.frames <- frame(wires[1])
	conn.frames <- []byte("--- Kết thúc lịch sử (2/3) ---\n")
	conn.frames <- frame(HistorySync{Type: "history_sync", Direction: "after", FirstSeq: 1, LastSeq: 2, NextSeq: 2, More: true, Sent: 2, Total: 2})
	conn.frames <- []byte("--- Lịch sử chat gần đây ---\n")
	conn.frames <- frame(wires[2])
	conn.frames <- []byte("--- Kết thúc lịch sử (1/1) ---\n")
	conn.frames <- frame(HistorySync{Type: "history_sync", Direction: "after", FirstSeq: 3, LastSeq: 3, NextSeq: 3, More: false, Sent: 1, Total: 1})

	go sess.runPump()
	waitHoldReleased(t, sess, 3*time.Second)
	close(conn.frames)
	close(sess.Quitting)
	<-sess.PumpDone

	conn.mu.Lock()
	defer conn.mu.Unlock()
	if len(conn.written) != 2 {
		t.Fatalf("load requests = %d, want 2", len(conn.written))
	}
	var r1, r2 HistoryRequest
	if err := json.Unmarshal(conn.written[0], &r1); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(conn.written[1], &r2); err != nil {
		t.Fatal(err)
	}
	if r1.AfterSeq == nil || *r1.AfterSeq != 0 {
		t.Fatalf("first page after_seq = %v, want 0", r1.AfterSeq)
	}
	if r2.AfterSeq == nil || *r2.AfterSeq != 2 {
		t.Fatalf("second page after_seq = %v, want 2", r2.AfterSeq)
	}
}

// TestLoadAnnouncesSync: the load prints a "Đang tải lịch sử" banner
// before the history so a slow load does not look like a hang, the
// banner precedes the loaded lines, page markers stay hidden, and a
// batched frame's trailing newline does not render a blank line.
func TestLoadAnnouncesSync(t *testing.T) {
	conn := &captureConn{frames: make(chan []byte, 16)}
	sess := recoverTestSession(t, 9, conn)
	sess.Display.TabSys = newTabBuffer(100, 100000)
	sess.Display.TabChat = newTabBuffer(100, 100000)
	var out bytes.Buffer
	sess.Display.Out = &out
	sess.Display.ActiveTab = TabChat
	anchor := [32]byte{9}
	wires := recoverTestChain(anchor, 1, 1)
	frame := func(v any) []byte {
		raw, err := json.Marshal(v)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		return raw
	}
	conn.frames <- frame(HistoryInfo{Type: "history_info", MinSeq: 1, MaxSeq: 1, MinHeight: 1, MaxHeight: 1, Count: 1})
	conn.frames <- []byte("--- Lịch sử chat gần đây ---\n")
	conn.frames <- frame(wires[0])
	conn.frames <- []byte("--- Kết thúc lịch sử (1/1) ---\n")
	conn.frames <- frame(HistorySync{Type: "history_sync", Direction: "after", FirstSeq: 1, LastSeq: 1, NextSeq: 1, Sent: 1, Total: 1})

	go sess.runPump()
	waitHoldReleased(t, sess, 3*time.Second)
	close(conn.frames)
	close(sess.Quitting)
	<-sess.PumpDone
	sess.flushOutputNow()

	text := out.String()
	bi := strings.Index(text, "| [Local]: Đang tải lịch sử")
	mi := strings.Index(text, "msg")
	if bi < 0 || mi < 0 || bi > mi {
		t.Fatalf("local sync banner must precede the loaded history: %q", text)
	}
	if strings.Contains(text, "Lịch sử chat gần đây") || strings.Contains(text, "Kết thúc lịch sử") {
		t.Fatalf("page markers must stay hidden during the load: %q", text)
	}
	if strings.Contains(text, "\n| \n") {
		t.Fatalf("batched frames must not render blank lines: %q", text)
	}
}

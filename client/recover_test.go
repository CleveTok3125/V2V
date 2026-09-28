package main

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
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

// A forward gap files one bounded request and skips the tamper latch.
func TestRecoveryRequestOnGap(t *testing.T) {
	conn := &captureConn{frames: make(chan []byte)}
	sess := recoverTestSession(t, 9, conn)

	sess.Display.DisplayMu.Lock()
	sess.checkChainLink(chainTestWire(103, 0x11))
	sess.Display.DisplayMu.Unlock()

	req := conn.lastRequest(t)
	if req.Type != "history_request" || req.After != 101 || req.Limit != 2 {
		t.Fatalf("request wrong: %+v", req)
	}
	p := sess.Chain.RecoverPending
	if p == nil || p.From != 101 || p.To != 102 || p.AnchorHeight != 100 || p.AnchorHash != [32]byte{9} {
		t.Fatalf("pending wrong: %+v", p)
	}
	if !strings.Contains(tabSysText(sess), "Đang bù 2 tin") {
		t.Fatalf("missing refill notice: %q", tabSysText(sess))
	}
	if sess.Chain.ChainGapWarned {
		t.Fatal("requested gap must not latch the tamper notice")
	}
	if sess.Chain.ChainHeight != 103 {
		t.Fatalf("tip must re-anchor to #103, got #%d", sess.Chain.ChainHeight)
	}
}

// A complete refill renders, indexes, verifies, and confirms —
// without moving the running tip.
func TestRecoveryWindowFlow(t *testing.T) {
	conn := &captureConn{frames: make(chan []byte)}
	sess := recoverTestSession(t, 9, conn)
	anchor := [32]byte{9}
	sess.Chain.RecoverPending = &recoverWindow{
		From: 101, To: 102, AnchorHeight: 100, AnchorHash: anchor,
		Next: 101, LastHash: anchor, RequestedAt: time.Now(),
	}
	wires := recoverTestChain(anchor, 101, 2)

	sess.Display.DisplayMu.Lock()
	sess.trackReplayWindow("--- Lịch sử bù ---", true)
	if !sess.Chain.InRecover {
		sess.Display.DisplayMu.Unlock()
		t.Fatal("recovery header must raise InRecover")
	}
	sess.verifyReplayWire(wires[0], true)
	sess.verifyReplayWire(wires[1], true)
	sess.trackReplayWindow("--- Kết thúc lịch sử bù (2/2) ---", false)
	if sess.Chain.InRecover {
		sess.Display.DisplayMu.Unlock()
		t.Fatal("recovery footer must clear InRecover")
	}
	sess.Display.DisplayMu.Unlock()
	sess.handleHistorySync(HistorySync{Type: "history_sync", MinHeight: 101, MaxHeight: 102, Sent: 2, Total: 2})

	for _, h := range []uint64{101, 102} {
		if _, ok := sess.Chain.WireIdx.get(h); !ok {
			t.Fatalf("recovered #%d must index", h)
		}
	}
	if sess.Chain.ChainTip != anchor || sess.Chain.ChainHeight != 100 {
		t.Fatal("recovery must not move the running tip")
	}
	sys := tabSysText(sess)
	if !strings.Contains(sys, "Đã bù 2/2 tin") {
		t.Fatalf("missing refill confirmation: %q", sys)
	}
	if strings.Contains(sys, "đứt") {
		t.Fatalf("refill must not warn tamper: %q", sys)
	}
	if sess.Chain.RecoverPending != nil {
		t.Fatal("completed refill must clear pending")
	}
}

// A wire that does not continue the anchor fails the window at the
// trailer without touching the tamper latch.
func TestRecoveryMismatchFails(t *testing.T) {
	conn := &captureConn{frames: make(chan []byte)}
	sess := recoverTestSession(t, 9, conn)
	anchor := [32]byte{9}
	sess.Chain.RecoverPending = &recoverWindow{
		From: 101, To: 102, AnchorHeight: 100, AnchorHash: anchor,
		Next: 101, LastHash: anchor, RequestedAt: time.Now(),
	}
	bad := chainTestWire(101, 0x11)

	sess.Display.DisplayMu.Lock()
	sess.trackReplayWindow("--- Lịch sử bù ---", true)
	sess.verifyReplayWire(bad, true)
	sess.trackReplayWindow("--- Kết thúc lịch sử bù (1/1) ---", false)
	sess.Display.DisplayMu.Unlock()
	sess.handleHistorySync(HistorySync{Type: "history_sync", MinHeight: 101, MaxHeight: 101, Sent: 1, Total: 1})

	sys := tabSysText(sess)
	if !strings.Contains(sys, "Không bù đủ") {
		t.Fatalf("missing refill failure: %q", sys)
	}
	if sess.Chain.ChainWarned {
		t.Fatal("bad refill wire must not latch tamper")
	}
	if _, ok := sess.Chain.WireIdx.get(101); ok {
		t.Fatal("bad refill wire must not index")
	}
}

// A gap beyond the cap keeps the notice-only path: no request.
func TestRecoveryCapExceeded(t *testing.T) {
	conn := &captureConn{frames: make(chan []byte)}
	sess := recoverTestSession(t, 9, conn)

	sess.Display.DisplayMu.Lock()
	sess.checkChainLink(chainTestWire(500, 0x11))
	sess.Display.DisplayMu.Unlock()

	conn.mu.Lock()
	n := len(conn.written)
	conn.mu.Unlock()
	if n != 0 {
		t.Fatalf("oversize gap must not request, sent %d", n)
	}
	if sess.Chain.RecoverPending != nil {
		t.Fatal("oversize gap must not pend")
	}
	if !sess.Chain.ChainGapWarned {
		t.Fatal("oversize gap must keep the light notice")
	}
}

// A second gap inside the timeout cannot widen the in-flight request
// (the server already capped its reply), so it is queued as a
// follow-up refill that fires when the first window settles.
func TestRecoveryMergesInflight(t *testing.T) {
	conn := &captureConn{frames: make(chan []byte)}
	sess := recoverTestSession(t, 9, conn)

	sess.Display.DisplayMu.Lock()
	sess.checkChainLink(chainTestWire(103, 0x11))
	sess.checkChainLink(chainTestWire(106, 0x22))
	sess.Display.DisplayMu.Unlock()

	req := conn.lastRequest(t)
	if req.After != 101 || req.Limit != 2 {
		t.Fatalf("first request must stay bounded: %+v", req)
	}
	p := sess.Chain.RecoverPending
	if p == nil || p.To != 102 {
		t.Fatalf("in-flight window must not be widened: %+v", p)
	}
	if p.FollowUp == nil || p.FollowUp.From != 104 || p.FollowUp.To != 105 {
		t.Fatalf("second gap must queue a follow-up: %+v", p.FollowUp)
	}

	// Settling the first window starts the follow-up against its own
	// anchor.
	sess.Display.DisplayMu.Lock()
	sess.finishRecovery()
	sess.Display.DisplayMu.Unlock()
	conn.mu.Lock()
	n := len(conn.written)
	conn.mu.Unlock()
	if n != 2 {
		t.Fatalf("follow-up must send a second request, sent %d", n)
	}
	var req2 HistoryRequest
	if err := json.Unmarshal(conn.written[1], &req2); err != nil || req2.After != 104 || req2.Limit != 2 {
		t.Fatalf("follow-up request wrong: %+v %v", req2, err)
	}
}

// A gap after the timeout replaces the stale window and retries.
func TestRecoveryTimeoutRetries(t *testing.T) {
	conn := &captureConn{frames: make(chan []byte)}
	sess := recoverTestSession(t, 9, conn)
	sess.Chain.RecoverPending = &recoverWindow{
		From: 101, To: 102, AnchorHeight: 100, AnchorHash: [32]byte{9},
		Next: 101, LastHash: [32]byte{9}, RequestedAt: time.Now().Add(-time.Minute),
	}

	sess.Display.DisplayMu.Lock()
	sess.checkChainLink(chainTestWire(106, 0x22))
	sess.Display.DisplayMu.Unlock()

	conn.mu.Lock()
	n := len(conn.written)
	conn.mu.Unlock()
	if n != 1 {
		t.Fatalf("timed-out window must retry, sent %d", n)
	}
	var req HistoryRequest
	if err := json.Unmarshal(conn.written[0], &req); err != nil || req.After != 101 || req.Limit != 5 {
		t.Fatalf("retry must cover #101–#105: %+v %v", req, err)
	}
	if p := sess.Chain.RecoverPending; p == nil || p.From != 101 || p.To != 105 {
		t.Fatalf("retry must replace pending: %+v", p)
	}
}

// Explicit zero disables auto-recovery.
func TestRecoveryDisabled(t *testing.T) {
	oldCfg := ClientCfg
	cfg := config.DefaultClientConfig()
	zero := 0
	cfg.History.RecoverCap = &zero
	ClientCfg = cfg
	defer func() { ClientCfg = oldCfg }()

	conn := &captureConn{frames: make(chan []byte)}
	sess := recoverTestSession(t, 9, conn)

	sess.Display.DisplayMu.Lock()
	sess.checkChainLink(chainTestWire(103, 0x11))
	sess.Display.DisplayMu.Unlock()

	conn.mu.Lock()
	n := len(conn.written)
	conn.mu.Unlock()
	if n != 0 {
		t.Fatal("disabled recovery must not request")
	}
}

// An exhausted window (nothing left in RAM) settles to failure.
func TestRecoveryExhausted(t *testing.T) {
	conn := &captureConn{frames: make(chan []byte)}
	sess := recoverTestSession(t, 9, conn)
	anchor := [32]byte{9}
	sess.Chain.RecoverPending = &recoverWindow{
		From: 101, To: 102, AnchorHeight: 100, AnchorHash: anchor,
		Next: 101, LastHash: anchor, RequestedAt: time.Now(),
	}

	sess.Display.DisplayMu.Lock()
	sess.trackReplayWindow("--- Lịch sử bù ---", true)
	sess.trackReplayWindow("--- Kết thúc lịch sử bù: không còn tin trong bộ nhớ ---", false)
	sess.Display.DisplayMu.Unlock()
	sess.handleHistorySync(HistorySync{Type: "history_sync", Sent: 0, Total: 0})

	sys := tabSysText(sess)
	if !strings.Contains(sys, "Không bù đủ") {
		t.Fatalf("exhausted refill must fail loudly: %q", sys)
	}
	if sess.Chain.RecoverPending != nil {
		t.Fatal("exhausted refill must clear pending")
	}
}

// TestRecoveryPumpRendersOnce drives the real pump path: a recovery
// window must render each refilled message exactly once. Regression
// for the double render where checkRecoverWire drew the wire and the
// pump drew it again.
func TestRecoveryPumpRendersOnce(t *testing.T) {
	conn := &captureConn{frames: make(chan []byte, 8)}
	sess := recoverTestSession(t, 9, conn)
	sess.Display.ActiveTab = TabChat
	sess.Display.TabSys = newTabBuffer(100, 100000)
	sess.Display.TabChat = newTabBuffer(100, 100000)
	anchor := [32]byte{9}
	sess.Chain.RecoverPending = &recoverWindow{
		From: 101, To: 102, AnchorHeight: 100, AnchorHash: anchor,
		Next: 101, LastHash: anchor, RequestedAt: time.Now(),
	}
	wires := recoverTestChain(anchor, 101, 2)
	frame := func(v any) []byte {
		raw, err := json.Marshal(v)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		return raw
	}
	conn.frames <- []byte("--- Lịch sử bù ---\n")
	conn.frames <- frame(wires[0])
	conn.frames <- frame(wires[1])
	conn.frames <- []byte("--- Kết thúc lịch sử bù (2/2) ---\n")
	conn.frames <- frame(HistorySync{Type: "history_sync", MinHeight: 101, MaxHeight: 102, Sent: 2, Total: 2})

	done := make(chan struct{})
	go func() { sess.runPump(); close(done) }()

	waitRecoverSettled(t, sess, 3*time.Second)
	close(conn.frames)
	close(sess.Quitting)
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("pump did not stop")
	}

	sess.Display.DisplayMu.Lock()
	got := 0
	for _, l := range sess.Display.TabChat.lines {
		if strings.Contains(l, "msg") {
			got++
		}
	}
	sess.Display.DisplayMu.Unlock()
	if got != 2 {
		t.Fatalf("recovered messages rendered %d head lines, want 2 (double render?)", got)
	}
}

// TestRecoveryPumpSkipsBadWire: a wire that does not continue the
// anchor must not be rendered or indexed by the pump either — only
// the window's failure notice reports it.
func TestRecoveryPumpSkipsBadWire(t *testing.T) {
	conn := &captureConn{frames: make(chan []byte, 8)}
	sess := recoverTestSession(t, 9, conn)
	sess.Display.ActiveTab = TabChat
	sess.Display.TabSys = newTabBuffer(100, 100000)
	sess.Display.TabChat = newTabBuffer(100, 100000)
	anchor := [32]byte{9}
	sess.Chain.RecoverPending = &recoverWindow{
		From: 101, To: 101, AnchorHeight: 100, AnchorHash: anchor,
		Next: 101, LastHash: anchor, RequestedAt: time.Now(),
	}
	bad := chainTestWire(101, 0x11)
	raw, err := json.Marshal(bad)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	conn.frames <- []byte("--- Lịch sử bù ---\n")
	conn.frames <- raw
	conn.frames <- []byte("--- Kết thúc lịch sử bù (1/1) ---\n")
	conn.frames <- []byte(`{"type":"history_sync","min_height":101,"max_height":101,"sent":1,"total":1}`)

	done := make(chan struct{})
	go func() { sess.runPump(); close(done) }()
	waitRecoverSettled(t, sess, 3*time.Second)
	close(conn.frames)
	close(sess.Quitting)
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("pump did not stop")
	}

	sess.Display.DisplayMu.Lock()
	defer sess.Display.DisplayMu.Unlock()
	if _, ok := sess.Chain.WireIdx.get(101); ok {
		t.Fatal("bad recovery wire must not index")
	}
	for _, l := range sess.Display.TabChat.lines {
		if strings.Contains(l, "msg") {
			t.Fatalf("bad recovery wire must not render: %q", l)
		}
	}
}

// TestRecoveryFromJoinTrailer: a join replay that lost lines must file
// one refill for exactly the missing heights, anchored on the drop's
// predecessor.
func TestRecoveryFromJoinTrailer(t *testing.T) {
	conn := &captureConn{frames: make(chan []byte)}
	sess := recoverTestSession(t, 9, conn)
	anchor := [32]byte{9}
	wires := recoverTestChain(anchor, 100, 5) // heights 100..104
	hashes := map[uint64][32]byte{}
	for _, w := range wires {
		h, _ := chain.ParseHex64(w.ChainHash)
		hashes[w.ChainHeight] = h
	}

	sess.Display.DisplayMu.Lock()
	sess.trackReplayWindow("--- Lịch sử chat gần đây ---", true)
	// 102 was dropped mid-replay.
	for _, h := range []uint64{100, 101, 103, 104} {
		sess.Chain.SyncHeights[h] = hashes[h]
	}
	sess.trackReplayWindow("--- Kết thúc lịch sử (4/5) ---", false)
	sess.Display.DisplayMu.Unlock()

	sess.handleHistorySync(HistorySync{Type: "history_sync", MinHeight: 100, MaxHeight: 104, Sent: 4, Total: 5, Dropped: 1})

	req := conn.lastRequest(t)
	if req.After != 102 || req.Limit != 3 {
		t.Fatalf("join refill must span #102–#104: %+v", req)
	}
	p := sess.Chain.RecoverPending
	if p == nil || p.From != 102 || p.To != 104 {
		t.Fatalf("pending wrong: %+v", p)
	}
	if len(p.Missing) != 1 || !p.Missing[102] {
		t.Fatalf("only #102 must be marked missing: %+v", p.Missing)
	}
	if p.LastHash != hashes[101] {
		t.Fatal("refill must anchor on the drop's predecessor")
	}
}

// TestRecoveryFromJoinTrailerRendersOnlyMissing: the refill span
// carries received heights too; those verify and advance the window
// but are not rendered again.
func TestRecoveryFromJoinTrailerRendersOnlyMissing(t *testing.T) {
	conn := &captureConn{frames: make(chan []byte, 8)}
	sess := recoverTestSession(t, 9, conn)
	sess.Display.TabSys = newTabBuffer(100, 100000)
	sess.Display.TabChat = newTabBuffer(100, 100000)
	var out bytes.Buffer
	sess.Display.Out = &out
	sess.Display.ActiveTab = TabChat
	anchor := [32]byte{9}
	wires := recoverTestChain(anchor, 100, 5)
	hashes := map[uint64][32]byte{}
	for _, w := range wires {
		h, _ := chain.ParseHex64(w.ChainHash)
		hashes[w.ChainHeight] = h
	}

	sess.Display.DisplayMu.Lock()
	sess.trackReplayWindow("--- Lịch sử chat gần đây ---", true)
	for _, h := range []uint64{100, 101, 103, 104} {
		sess.Chain.SyncHeights[h] = hashes[h]
	}
	sess.trackReplayWindow("--- Kết thúc lịch sử (4/5) ---", false)
	sess.Display.DisplayMu.Unlock()
	sess.handleHistorySync(HistorySync{Type: "history_sync", MinHeight: 100, MaxHeight: 104, Sent: 4, Total: 5, Dropped: 1})

	frame := func(v any) []byte {
		raw, err := json.Marshal(v)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		return raw
	}
	conn.frames <- []byte("--- Lịch sử bù ---\n")
	conn.frames <- frame(wires[2]) // 102 (missing)
	conn.frames <- frame(wires[3]) // 103 (already received)
	conn.frames <- frame(wires[4]) // 104 (already received)
	conn.frames <- []byte("--- Kết thúc lịch sử bù (3/3) ---\n")
	conn.frames <- frame(HistorySync{Type: "history_sync", MinHeight: 102, MaxHeight: 104, Sent: 3, Total: 3})

	done := make(chan struct{})
	go func() { sess.runPump(); close(done) }()
	waitRecoverSettled(t, sess, 3*time.Second)
	close(conn.frames)
	close(sess.Quitting)
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("pump did not stop")
	}

	sess.Display.DisplayMu.Lock()
	defer sess.Display.DisplayMu.Unlock()
	got := 0
	for _, l := range sess.Display.TabChat.lines {
		if strings.Contains(l, "msg") {
			got++
		}
	}
	if got != 1 {
		t.Fatalf("refill rendered %d head lines, want 1 (only the missing #102)", got)
	}
	if _, ok := sess.Chain.WireIdx.get(102); !ok {
		t.Fatal("missing #102 must index")
	}
	sess.flushOutputNow()
	text := out.String()
	fi := strings.Index(text, "Kết thúc lịch sử bù")
	ci := strings.Index(text, "Đã bù")
	if fi < 0 || ci < 0 || fi > ci {
		t.Fatalf("recovery footer must precede the confirmation: %q", text)
	}
}

// A join replay whose refill span exceeds the cap keeps the notice.
func TestRecoveryFromJoinTrailerCap(t *testing.T) {
	conn := &captureConn{frames: make(chan []byte)}
	sess := recoverTestSession(t, 9, conn)
	sess.Chain.SyncHeights = map[uint64][32]byte{100: {9}}
	sess.Chain.SyncClosed = true

	sess.handleHistorySync(HistorySync{Type: "history_sync", MinHeight: 100, MaxHeight: 500, Dropped: 1})

	conn.mu.Lock()
	n := len(conn.written)
	conn.mu.Unlock()
	if n != 0 {
		t.Fatalf("oversize join gap must not request, sent %d", n)
	}
	if sess.Chain.RecoverPending != nil {
		t.Fatal("oversize join gap must not pend")
	}
	if !strings.Contains(tabSysText(sess), "Bỏ lỡ") {
		t.Fatalf("oversize join gap must notice: %q", tabSysText(sess))
	}
}

// TestHandleHistorySyncForkScope pins the fork check to the join
// trailer: segment and recovery trailers have no received-height set,
// so comparing the persisted tip against the join's would only warn
// falsely.
func TestHandleHistorySyncForkScope(t *testing.T) {
	newSess := func() *Session {
		s := recoverTestSession(t, 9, &captureConn{frames: make(chan []byte)})
		s.Display.TabSys = newTabBuffer(100, 100000)
		s.Chain.HavePersistedTip = true
		s.Chain.PersistedTip = [32]byte{0xaa}
		s.Chain.PersistedHeight = 120
		return s
	}

	// Segment trailer: no warning.
	seg := newSess()
	seg.Display.DisplayMu.Lock()
	seg.trackReplayWindow("--- Lịch sử cũ ---", true)
	seg.trackReplayWindow("--- Kết thúc lịch sử cũ (2/2) ---", false)
	seg.Display.DisplayMu.Unlock()
	seg.handleHistorySync(HistorySync{Type: "history_sync", MinHeight: 100, MaxHeight: 130, Sent: 2, Total: 2})
	if strings.Contains(tabSysText(seg), "phân nhánh") {
		t.Fatalf("segment trailer must not warn: %q", tabSysText(seg))
	}

	// Join trailer with the same height rewritten: warns.
	join := newSess()
	join.Display.DisplayMu.Lock()
	join.trackReplayWindow("--- Lịch sử chat gần đây ---", true)
	join.Chain.SyncHeights[120] = [32]byte{0xbb}
	join.trackReplayWindow("--- Kết thúc lịch sử (2/2) ---", false)
	join.Display.DisplayMu.Unlock()
	join.handleHistorySync(HistorySync{Type: "history_sync", MinHeight: 100, MaxHeight: 130, Sent: 2, Total: 2})
	if !strings.Contains(tabSysText(join), "phân nhánh") {
		t.Fatalf("join trailer must warn on a rewritten height: %q", tabSysText(join))
	}
}

// Recovery boundaries track their own flag, independent of the join
// and segment windows.
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

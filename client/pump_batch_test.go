package main

import (
	"encoding/hex"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/CleveTok3125/V2V/internal/chain"
)

// pumpFeedConn is a fake wsConn that replays queued frames, then parks
// until released (which surfaces an error so runPump can exit cleanly
// once the test closes Quitting).
type pumpFeedConn struct {
	frames  chan []byte
	release chan struct{}
}

func (c *pumpFeedConn) ReadJSON(v any) error { return io.EOF }
func (c *pumpFeedConn) WriteJSON(v any) error {
	return io.EOF
}
func (c *pumpFeedConn) ReadMessage() (int, []byte, error) {
	select {
	case m, ok := <-c.frames:
		if !ok {
			return 0, nil, io.EOF
		}
		return 1, m, nil
	case <-c.release:
		return 0, nil, io.EOF
	}
}
func (c *pumpFeedConn) WriteMessage(int, []byte) error { return io.EOF }
func (c *pumpFeedConn) SetReadLimit(int64)             {}
func (c *pumpFeedConn) Close() error                   { return nil }

// slowWriter simulates a sluggish terminal: every Write parks, so any
// read-loop write blocks the socket drain — exactly how a slow peer
// fills the server's per-session queue until frames drop.
type slowWriter struct {
	delay time.Duration
	data  []byte
}

func (w *slowWriter) Write(p []byte) (int, error) {
	time.Sleep(w.delay)
	w.data = append(w.data, p...)
	return len(p), nil
}

// pumpBatchWire builds a properly chained wire sequence so verification
// passes and only terminal I/O slows the pump.
func pumpBatchWire(prev [32]byte, height uint64) (WireMessage, [32]byte) {
	wire := WireMessage{Type: "chat", Time: "12:00", DisplayName: "A", Text: "msg", ChainHeight: height, ChainVer: 2}
	wire.ChainPrev = hex.EncodeToString(prev[:])
	h := chain.Hash(prev, height, 0, 0, "chat", "12:00", "A", "msg", "")
	wire.ChainHash = hex.EncodeToString(h[:])
	return wire, h
}

// TestRunPumpKeepsReadingWithSlowOutput: a slow terminal must not stall
// the socket read loop. The pump has to ingest a burst of valid wires
// on schedule even when every terminal write parks; otherwise the
// server's per-session queue fills and live frames drop.
func TestRunPumpKeepsReadingWithSlowOutput(t *testing.T) {
	const count = 100
	const budget = 600 * time.Millisecond

	sess := NewSession()
	slow := &slowWriter{delay: 20 * time.Millisecond}
	sess.Display.Out = slow
	sess.Display.Term = &fakeTerm{}
	sess.Display.ActiveTab = TabChat
	sess.Display.TabSys = newTabBuffer(100, 100000)
	sess.Display.TabChat = newTabBuffer(100, 100000)
	sess.Chain.TipPath = t.TempDir() + "/tip.json"
	sess.Chain.WireIdx = newWireIndex(count + 16)
	sess.Chain.RenderCache = newRenderCache(8)
	sess.Username = "PumpTest"
	conn := &pumpFeedConn{frames: make(chan []byte, count), release: make(chan struct{})}
	sess.Conn = conn

	var prev [32]byte
	for h := uint64(1); h <= count; h++ {
		wire, next := pumpBatchWire(prev, h)
		prev = next
		raw, err := json.Marshal(wire)
		if err != nil {
			t.Fatalf("marshal wire: %v", err)
		}
		conn.frames <- raw
	}

	go sess.runPump()
	deadline := time.Now().Add(budget)
	for {
		// The index is pump-owned: read it under DisplayMu like the
		// input loop does, so -race stays clean.
		sess.Display.DisplayMu.Lock()
		got := 0
		for h := uint64(1); h <= count; h++ {
			if _, ok := sess.Chain.WireIdx.get(h); ok {
				got++
			}
		}
		sess.Display.DisplayMu.Unlock()
		if got == count {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("pump indexed %d/%d wires in %v: terminal writes stall the read loop", got, count, budget)
		}
		time.Sleep(5 * time.Millisecond)
	}
	close(sess.Quitting)
	close(conn.release)
	<-sess.PumpDone
	// Queued output flushes on demand: all ingested lines must land
	// exactly once, regardless of terminal speed.
	sess.flushOutputNow()
	if got := strings.Count(string(slow.data), "msg"); got != count {
		t.Fatalf("flushed terminal output = %d/%d lines", got, count)
	}
}

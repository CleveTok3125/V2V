package main

import (
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestChainTipConcurrent races the pump tip path against the input
// loop flush the way production does: pump holds DisplayMu around
// note/check (nesting ChainMu), input flushes lock-free (ChainMu
// only). Under -race any shared-state violation fails; the timeout
// turns a lock-order deadlock into a failure instead of a hang.
func TestChainTipConcurrent(t *testing.T) {
	sess := NewSession()
	sess.Display.Out = io.Discard
	sess.Display.Term = &fakeTerm{}
	sess.Display.TabChat = newTabBuffer(100, 100000)
	sess.Display.TabSys = newTabBuffer(100, 100000)
	sess.Chain.TipPath = t.TempDir() + "/tip.json"

	wire := WireMessage{
		Type: "chat", Text: "hi",
		ChainHash: strings.Repeat("ab", 32),
	}
	var wg sync.WaitGroup
	for g := 0; g < 4; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 30; i++ {
				var tip [32]byte
				tip[0], tip[1] = byte(g), byte(i)
				sess.Display.DisplayMu.Lock()
				sess.noteChainTip(tip, uint64(i))
				sess.checkChainLink(wire)
				sess.Display.DisplayMu.Unlock()
				sess.flushChainTip()
			}
		}(g)
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("chain tip paths deadlocked")
	}
}

// TestPendingTrackConcurrent races placeholder tracking (single input
// loop, like production: sendMessage plus lock-free flushes) against
// pump-side echo consumption (DisplayMu-held, like pump.go). wg.Wait is
// bounded by a 30s timeout so a deadlock fails loudly instead of hanging.
func TestPendingTrackConcurrent(t *testing.T) {
	sess := NewSession()
	sess.Display.Out = io.Discard
	sess.Display.Term = &fakeTerm{}
	sess.Display.TabChat = newTabBuffer(100, 100000)
	sess.Display.TabSys = newTabBuffer(100, 100000)
	sess.Conn = &stubConn{}
	sess.Username = "me"

	stray := WireMessage{Type: "chat", Text: "stray", DisplayName: "other", TmpID: 999}
	var wg sync.WaitGroup
	// Single sender: production has one input loop per session, so
	// concurrent sendMessage on one session (and its stub conn) is
	// out of scope by construction.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 30; i++ {
			if err := sess.sendMessage("hi", 0, false, 0); err != nil {
				t.Errorf("sendMessage: %v", err)
				return
			}
			sess.flushChainTip()
		}
	}()
	for g := 0; g < 2; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 30; i++ {
				sess.Display.DisplayMu.Lock()
				sess.consumeEchoLocked(stray, true)
				sess.Display.DisplayMu.Unlock()
			}
		}()
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("pending track paths deadlocked")
	}
}

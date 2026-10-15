package main

import (
	"encoding/hex"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/CleveTok3125/V2V/lib/chain"
	"github.com/CleveTok3125/V2V/lib/wire"
)

// TestChainTipConcurrent races the pump tip path against the input
// loop flush the way production does: pump holds DisplayMu around
// note/check (nesting ChainMu), input flushes lock-free (ChainMu
// only). Under -race any shared-state violation fails; the timeout
// turns a lock-order deadlock into a failure instead of a hang.
func TestChainTipConcurrent(t *testing.T) {
	sess := NewSession()
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
			if err := sess.sendMessage("hi", entry{}, slotRef{}, false); err != nil {
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

// TestCheckChainLinkSuccessPath drives the branch the error-path
// tests never reach: a well-formed first link verifies, notes the
// tip and warns nothing.
func TestCheckChainLinkSuccessPath(t *testing.T) {
	sess := NewSession()
	sess.Display.Term = &fakeTerm{}
	sess.Display.TabSys = newTabBuffer(100, 100000)
	var prev [32]byte
	wire := WireMessage{Type: "chat", Time: "12:00", DisplayName: "A", Text: "hi", ChainHeight: 1, ChainVer: 2}
	wire.ChainPrev = hex.EncodeToString(prev[:])
	h := chain.Hash(prev, 1, 0, 0, "chat", "12:00", "A", "hi", "")
	wire.ChainHash = hex.EncodeToString(h[:])
	sess.Display.DisplayMu.Lock()
	sess.checkChainLink(wire)
	sess.Display.DisplayMu.Unlock()
	if !sess.Chain.ChainHaveTip || sess.Chain.ChainTip != h || sess.Chain.ChainHeight != 1 {
		t.Fatalf("tip not adopted: haveTip=%v tip=%x height=%d", sess.Chain.ChainHaveTip, sess.Chain.ChainTip, sess.Chain.ChainHeight)
	}
	if sess.Chain.ChainWarned {
		t.Fatal("success path must not warn")
	}
}

// TestConsumeEchoMatchPath drives the hit branch the stray test
// never reaches: a matching echo splices its placeholder without
// touching the screen (shown=false), exercising the queue splice
// and erase decision under the real nesting.
func TestConsumeEchoMatchPath(t *testing.T) {
	sess := NewSession()
	sess.Display.Term = &fakeTerm{}
	sess.Display.TabChat = newTabBuffer(100, 100000)
	sess.Display.TabSys = newTabBuffer(100, 100000)
	sess.Username = "me"
	sess.Pending.PendingPlaceholders = []pendingMsg{
		{text: "hi", tmpID: 7, shown: false},
	}
	sess.Display.DisplayMu.Lock()
	sess.consumeEchoLocked(WireMessage{Type: "chat", Text: "hi", DisplayName: "me", TmpID: 7}, true)
	sess.Display.DisplayMu.Unlock()
	if len(sess.Pending.PendingPlaceholders) != 0 {
		t.Fatalf("matched placeholder not spliced: %+v", sess.Pending.PendingPlaceholders)
	}
}

// TestNotifyGateConcurrent races the render-side notice gate against the
// /notify switches the way production does: the pump gates a system wire
// while the input loop flips a tag, replaces the quiet set and moves the
// tier floor. The gate state holds a map, so a gate read outside NotifyMu
// races those writes; only -race sees it, while the timeout still turns a
// deadlock into a failure.
func TestNotifyGateConcurrent(t *testing.T) {
	sess := NewSession()
	sess.Display.Term = &fakeTerm{}
	sess.Display.Notify = NotifyState{Muted: map[string]bool{}, PowMinTier: 1}
	msg := WireMessage{Type: "system", Tags: wire.WithTags(wire.TagJoin), Text: "x"}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 500; i++ {
			_ = sess.notifyWireAllowed(msg)
			_ = sess.notifyPowLive(i % 3)
			_ = sess.notifyTagsAllowed(wire.WithTags(wire.TagJoin))
			_ = sess.notifyTagEnabled(wire.TagJoin)
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 500; i++ {
			sess.notifySetTag(wire.TagJoin, i%2 == 0)
			sess.notifySetAll(i%3 != 0)
			sess.notifySetPowMinTier(i % 3)
		}
	}()
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("notify gate paths deadlocked")
	}
}

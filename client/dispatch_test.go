package main

import (
	"io"
	"testing"
	"time"
)

func sessionForDispatch(t *testing.T) *Session {
	t.Helper()
	sess := NewSession()
	sess.Out = io.Discard
	sess.Term = &fakeTerm{}
	sess.TabChat = newTabBuffer(100, 100000)
	sess.TabSys = newTabBuffer(100, 100000)
	sess.WireIdx = newWireIndex(16)
	return sess
}

func seedChatHeight(sess *Session, height uint64) {
	sess.TabChat.append("| Alice: hi")
	sess.TabChat.append(metaLineFor(height, "abcd0000", ""))
}

func TestDispatchInlineReplyPassesBody(t *testing.T) {
	sess := sessionForDispatch(t)
	seedChatHeight(sess, 1234)
	text, act := sess.dispatch("/reply 1234 đồng ý")
	if act != cmdPass {
		t.Fatalf("act=%v want cmdPass", act)
	}
	if text != "đồng ý" {
		t.Fatalf("text=%q", text)
	}
	if sess.PendingReplyTo != 1234 {
		t.Fatalf("PendingReplyTo=%d", sess.PendingReplyTo)
	}
}

func TestDispatchReplyDraftDoesNotSend(t *testing.T) {
	sess := sessionForDispatch(t)
	seedChatHeight(sess, 1234)
	text, act := sess.dispatch("/reply 1234")
	if act != cmdDone {
		t.Fatalf("act=%v want cmdDone", act)
	}
	if text != "/reply 1234" {
		t.Fatalf("text=%q", text)
	}
	if sess.ReplyDraft != 1234 {
		t.Fatalf("ReplyDraft=%d", sess.ReplyDraft)
	}
	if sess.PendingReplyTo != 0 {
		t.Fatalf("PendingReplyTo=%d", sess.PendingReplyTo)
	}
}

func TestDispatchReplyMissingHeight(t *testing.T) {
	sess := sessionForDispatch(t)
	_, act := sess.dispatch("/reply 9999 hi")
	if act != cmdDone {
		t.Fatalf("act=%v want cmdDone", act)
	}
	if sess.PendingReplyTo != 0 {
		t.Fatalf("PendingReplyTo=%d", sess.PendingReplyTo)
	}
}

func TestDispatchReplyBadArg(t *testing.T) {
	sess := sessionForDispatch(t)
	_, act := sess.dispatch("/reply")
	if act != cmdDone {
		t.Fatalf("act=%v want cmdDone", act)
	}
}

func TestDispatchPlainChatPasses(t *testing.T) {
	sess := sessionForDispatch(t)
	text, act := sess.dispatch("hello")
	if act != cmdPass || text != "hello" {
		t.Fatalf("got %q %v", text, act)
	}
}

func TestDispatchInlineReplyCommandBodyClearsPending(t *testing.T) {
	sess := sessionForDispatch(t)
	seedChatHeight(sess, 1234)
	_, act := sess.dispatch("/reply 1234 /meta")
	if act != cmdDone {
		t.Fatalf("act=%v want cmdDone", act)
	}
	if sess.PendingReplyTo != 0 {
		t.Fatalf("PendingReplyTo=%d want 0 (command body must not leak quote)", sess.PendingReplyTo)
	}
}

func TestDispatchInlineReplyUnknownBodyClearsPending(t *testing.T) {
	sess := sessionForDispatch(t)
	seedChatHeight(sess, 1234)
	_, act := sess.dispatch("/reply 1234 /clear")
	if act != cmdDone {
		t.Fatalf("act=%v want cmdDone", act)
	}
	if sess.PendingReplyTo != 0 {
		t.Fatalf("PendingReplyTo=%d want 0 (rejected body must not leak quote)", sess.PendingReplyTo)
	}
}

func TestGracefulQuitWaitsForPump(t *testing.T) {
	// Exited pump: PumpDone is already closed, so gracefulQuit must
	// return promptly instead of hanging. The bound is deliberately
	// generous (2x the 500ms cap): it catches hangs, not sub-cap
	// precision — the cap itself is pinned by
	// TestGracefulQuitBoundsMissingPump.
	sess := sessionForDispatch(t)
	sess.Conn = &stubConn{}
	close(sess.PumpDone)
	start := time.Now()
	sess.gracefulQuit()
	if time.Since(start) > time.Second {
		t.Fatal("gracefulQuit with exited pump must not hang")
	}
	select {
	case <-sess.Quitting:
	default:
		t.Fatal("gracefulQuit must signal Quitting")
	}
}

func TestGracefulQuitBoundsMissingPump(t *testing.T) {
	// Pump never exits: the 500ms cap bounds the wait, never hangs.
	// Lower bound sits 50ms under the cap (timers do not fire early);
	// upper bound is generous for slow CI runners.
	sess := sessionForDispatch(t)
	sess.Conn = &stubConn{}
	start := time.Now()
	sess.gracefulQuit()
	if d := time.Since(start); d < 450*time.Millisecond || d > 5*time.Second {
		t.Fatalf("gracefulQuit without pump exit took %v, want ~500ms cap", d)
	}
}

func TestGracefulQuitNilPumpDone(t *testing.T) {
	// Test-built sessions without a pump skip the wait entirely.
	sess := &Session{Out: io.Discard, Term: &fakeTerm{}, Conn: &stubConn{}, Quitting: make(chan bool, 1), VerifyCh: make(chan verifyJob, 1)}
	start := time.Now()
	sess.gracefulQuit()
	if time.Since(start) > time.Second {
		t.Fatal("gracefulQuit with nil PumpDone must not wait")
	}
}

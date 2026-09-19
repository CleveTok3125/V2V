package main

import (
	"io"
	"testing"
	"time"
)

func sessionForDispatch(t *testing.T) *Session {
	t.Helper()
	sess := NewSession()
	sess.Display.Out = io.Discard
	sess.Display.Term = &fakeTerm{}
	sess.Display.TabChat = newTabBuffer(100, 100000)
	sess.Display.TabSys = newTabBuffer(100, 100000)
	sess.Chain.WireIdx = newWireIndex(16)
	return sess
}

func seedChatHeight(sess *Session, height uint64) {
	sess.Display.TabChat.append("| Alice: hi")
	sess.Display.TabChat.append(metaLineFor(height, "abcd0000", ""))
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
	if sess.Pending.PendingReplyTo != 1234 {
		t.Fatalf("PendingReplyTo=%d", sess.Pending.PendingReplyTo)
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
	if sess.Pending.ReplyDraft != 1234 {
		t.Fatalf("ReplyDraft=%d", sess.Pending.ReplyDraft)
	}
	if sess.Pending.PendingReplyTo != 0 {
		t.Fatalf("PendingReplyTo=%d", sess.Pending.PendingReplyTo)
	}
}

func TestDispatchReplyMissingHeight(t *testing.T) {
	sess := sessionForDispatch(t)
	_, act := sess.dispatch("/reply 9999 hi")
	if act != cmdDone {
		t.Fatalf("act=%v want cmdDone", act)
	}
	if sess.Pending.PendingReplyTo != 0 {
		t.Fatalf("PendingReplyTo=%d", sess.Pending.PendingReplyTo)
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
	if sess.Pending.PendingReplyTo != 0 {
		t.Fatalf("PendingReplyTo=%d want 0 (command body must not leak quote)", sess.Pending.PendingReplyTo)
	}
}

func TestDispatchInlineReplyUnknownBodyClearsPending(t *testing.T) {
	sess := sessionForDispatch(t)
	seedChatHeight(sess, 1234)
	_, act := sess.dispatch("/reply 1234 /clear")
	if act != cmdDone {
		t.Fatalf("act=%v want cmdDone", act)
	}
	if sess.Pending.PendingReplyTo != 0 {
		t.Fatalf("PendingReplyTo=%d want 0 (rejected body must not leak quote)", sess.Pending.PendingReplyTo)
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
	sess := &Session{
		Conn:     &stubConn{},
		Quitting: make(chan bool, 1),
		Display:  DisplayState{Out: io.Discard, Term: &fakeTerm{}},
		Verify:   VerifyState{VerifyCh: make(chan verifyJob, 1)},
	}
	start := time.Now()
	sess.gracefulQuit()
	if time.Since(start) > time.Second {
		t.Fatal("gracefulQuit with nil PumpDone must not wait")
	}
}

func seedWireIdx(sess *Session, heights ...uint64) {
	for _, h := range heights {
		sess.Chain.WireIdx.put(WireMessage{Type: "chat", Text: "t", ChainHeight: h, ChainHash: "ab"})
	}
}

func TestCmdOlderSendsRequest(t *testing.T) {
	sess := sessionForDispatch(t)
	stub := &stubConn{}
	sess.Conn = stub
	seedWireIdx(sess, 5, 6, 7)
	if !sess.cmdOlder("/older") {
		t.Fatal("/older must match")
	}
	req, ok := stub.wrote.(HistoryRequest)
	if !ok {
		t.Fatalf("wrote = %#v, want HistoryRequest", stub.wrote)
	}
	if req.Type != "history_request" || req.Before != 5 || req.Limit != 50 {
		t.Fatalf("request = %+v, want before=5 limit=50", req)
	}
}

func TestCmdOlderCustomLimit(t *testing.T) {
	sess := sessionForDispatch(t)
	stub := &stubConn{}
	sess.Conn = stub
	seedWireIdx(sess, 9)
	if !sess.cmdOlder("/older 10") {
		t.Fatal("/older 10 must match")
	}
	req, ok := stub.wrote.(HistoryRequest)
	if !ok || req.Before != 9 || req.Limit != 10 {
		t.Fatalf("request = %#v", stub.wrote)
	}
}

func TestCmdOlderRejectsBadArg(t *testing.T) {
	sess := sessionForDispatch(t)
	stub := &stubConn{}
	sess.Conn = stub
	seedWireIdx(sess, 9)
	for _, bad := range []string{"/older abc", "/older 0", "/older -3"} {
		stub.wrote = nil
		if !sess.cmdOlder(bad) {
			t.Fatalf("%q must match", bad)
		}
		if stub.wrote != nil {
			t.Fatalf("%q must not send, wrote %#v", bad, stub.wrote)
		}
	}
}

func TestCmdOlderEmptyIndex(t *testing.T) {
	sess := sessionForDispatch(t)
	stub := &stubConn{}
	sess.Conn = stub
	if !sess.cmdOlder("/older") {
		t.Fatal("/older must match")
	}
	if stub.wrote != nil {
		t.Fatalf("empty index must not send, wrote %#v", stub.wrote)
	}
}

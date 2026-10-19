package main

import (
	"strings"
	"testing"

	"github.com/CleveTok3125/V2V/lib/wire"
)

func voteWire(target uint64, up, down int) WireMessage {
	return WireMessage{
		Type: "system", Tags: wire.WithTags(wire.TagVote),
		Text: "fallback", VoteTarget: target, VoteUp: up, VoteDown: down,
	}
}

func seedVotableChat(sess *Session, height uint64) {
	sess.Chain.WireIdx.put(WireMessage{Type: "chat", Text: "hi", ChainHeight: height, ChainHash: "abcd1234"})
	sess.Display.TabChat.append(chatBlockForTest(height, "abcd1234", "15:04", "Alice", "hi"))
}

func TestIsVoteWire(t *testing.T) {
	if !isVoteWire(voteWire(3, 1, 0)) {
		t.Fatal("tagged vote with target must match")
	}
	if isVoteWire(WireMessage{Type: "system", Tags: wire.WithTags(wire.TagVote), Text: "no target"}) {
		t.Fatal("target 0 must not match")
	}
	if isVoteWire(WireMessage{Type: "system", Text: "untagged", VoteTarget: 3}) {
		t.Fatal("untagged line must not match")
	}
	if isVoteWire(WireMessage{Type: "chat", Text: "hi", ChainHeight: 3}) {
		t.Fatal("chat must not match")
	}
}

func TestVoteSuffixEmpty(t *testing.T) {
	sess := NewSession()
	if got := sess.voteSuffixFor(5); got != "" {
		t.Fatalf("no votes must render empty, got %q", got)
	}
	sess.Chain.Votes[5] = VoteScore{}
	if got := sess.voteSuffixFor(5); got != "" {
		t.Fatalf("zero totals must render empty, got %q", got)
	}
}

func TestApplyVoteRepaintsTarget(t *testing.T) {
	sess, _ := queueTestSession(t)
	sess.Chain.WireIdx = newWireIndex(16)
	sess.Chain.RenderCache = newRenderCache(16)
	sess.Display.ShowMeta = true
	seedVotableChat(sess, 42)
	sess.Display.DisplayMu.Lock()
	if !sess.applyVoteLocked(voteWire(42, 12, 3)) {
		sess.Display.DisplayMu.Unlock()
		t.Fatal("vote wire must apply")
	}
	got := sess.Chain.Votes[42]
	if got.Up != 12 || got.Down != 3 {
		sess.Display.DisplayMu.Unlock()
		t.Fatalf("map = %+v, want 12/3", got)
	}
	header := sess.Display.TabChat.lines[0].header
	sess.Display.DisplayMu.Unlock()
	if !strings.Contains(header, "▲12") || !strings.Contains(header, "▼3") {
		t.Fatalf("header %q must carry the score", header)
	}
}

func TestApplyVoteEvictedKeepsMap(t *testing.T) {
	sess, _ := queueTestSession(t)
	sess.Chain.WireIdx = newWireIndex(16)
	sess.Display.DisplayMu.Lock()
	if !sess.applyVoteLocked(voteWire(99, 0, 4)) {
		sess.Display.DisplayMu.Unlock()
		t.Fatal("vote wire must apply")
	}
	got := sess.Chain.Votes[99]
	sess.Display.DisplayMu.Unlock()
	if got.Up != 0 || got.Down != 4 {
		t.Fatalf("map = %+v, want 0/4", got)
	}
}

func TestCmdVoteSendsRequest(t *testing.T) {
	sess := sessionForDispatch(t)
	stub := &stubConn{}
	sess.Conn = stub
	sess.Chain.WireIdx.put(WireMessage{Type: "chat", Text: "hi", ChainHeight: 7, ChainHash: "abcd"})
	seedChatHeight(sess, 7)
	if !sess.cmdVote("/up 7") {
		t.Fatal("/up must match")
	}
	req, ok := stub.wrote.(VoteRequest)
	if !ok || req.Type != "vote" || req.Target != 7 || req.Dir != 1 {
		t.Fatalf("wrote = %#v", stub.wrote)
	}
	stub.wrote = nil
	if !sess.cmdVote("/down 7") {
		t.Fatal("/down must match")
	}
	if req, ok := stub.wrote.(VoteRequest); !ok || req.Dir != -1 {
		t.Fatalf("wrote = %#v", stub.wrote)
	}
	stub.wrote = nil
	if !sess.cmdVote("/unvote 7") {
		t.Fatal("/unvote must match")
	}
	if req, ok := stub.wrote.(VoteRequest); !ok || req.Dir != 0 {
		t.Fatalf("wrote = %#v", stub.wrote)
	}
}

func TestCmdVoteRejectsEvicted(t *testing.T) {
	sess := sessionForDispatch(t)
	stub := &stubConn{}
	sess.Conn = stub
	if !sess.cmdVote("/up 9999") {
		t.Fatal("/up must match")
	}
	if stub.wrote != nil {
		t.Fatalf("evicted target must not send, wrote %#v", stub.wrote)
	}
}

package main

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/CleveTok3125/V2V/lib/wire"
)

func voteWire(target uint64, upvotes, downvotes int) WireMessage {
	return WireMessage{
		Type: "system", Tags: wire.WithTags(wire.TagVote),
		Text: "fallback", VoteTarget: target, VoteUpvotes: upvotes, VoteDownvotes: downvotes,
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

func TestVoteStripEmpty(t *testing.T) {
	sess := NewSession()
	if got := sess.voteStripFor(0); got != "" {
		t.Fatalf("height 0 must render no strip, got %q", got)
	}
	if got := sess.voteStripFor(5); got != "▲0 ▼0" {
		t.Fatalf("fresh message must show zero counts, got %q", got)
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
	if got.Upvotes != 12 || got.Downvotes != 3 {
		sess.Display.DisplayMu.Unlock()
		t.Fatalf("map = %+v, want 12/3", got)
	}
	header := sess.Display.TabChat.lines[0].header
	sess.Display.DisplayMu.Unlock()
	if !strings.Contains(header, "▲12 ▼3") {
		t.Fatalf("header %q must carry the strip", header)
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
	if got.Upvotes != 0 || got.Downvotes != 4 {
		t.Fatalf("map = %+v, want 0/4", got)
	}
}

func TestCmdVoteSendsRequest(t *testing.T) {
	sess := sessionForDispatch(t)
	stub := &stubConn{}
	sess.Conn = stub
	sess.Chain.WireIdx.put(WireMessage{Type: "chat", Text: "hi", ChainHeight: 7, ChainHash: "abcd"})
	seedChatHeight(sess, 7)
	for _, tc := range []struct {
		input     string
		direction int
	}{
		{"/upvote 7", wire.DirUpvote},
		{"/up 7", wire.DirUpvote},
		{"/downvote 7", wire.DirDownvote},
		{"/down 7", wire.DirDownvote},
		{"/unvote 7", wire.DirWithdraw},
	} {
		stub.wrote = nil
		if !sess.cmdVote(tc.input) {
			t.Fatalf("%q must match", tc.input)
		}
		req, ok := stub.wrote.(VoteRequest)
		if !ok || req.Type != "vote" || req.Target != 7 || req.Direction != tc.direction {
			t.Fatalf("%q wrote = %#v", tc.input, stub.wrote)
		}
	}
}

func TestCmdVoteRejectsEvicted(t *testing.T) {
	sess := sessionForDispatch(t)
	stub := &stubConn{}
	sess.Conn = stub
	if !sess.cmdVote("/upvote 9999") {
		t.Fatal("/upvote must match")
	}
	if stub.wrote != nil {
		t.Fatalf("evicted target must not send, wrote %#v", stub.wrote)
	}
}

func TestVoteOnLine(t *testing.T) {
	line := "15:04 Alice • #42:abcd ▲12 ▼3"
	height, direction, start, end, ok := voteOnLine(line)
	if !ok || height != 42 || direction != wire.DirUpvote {
		t.Fatalf("up token = %d/%d %v", height, direction, ok)
	}
	if start < 0 || end <= start {
		t.Fatalf("span = [%d,%d)", start, end)
	}
	spans := voteSpansOnLine(line)
	if len(spans) != 2 || spans[1].direction != wire.DirDownvote || spans[1].height != 42 {
		t.Fatalf("both tokens must map to #42: %+v", spans)
	}
	// Arrow runes before the reference belong to the author, never to
	// the strip: legacy display names may carry them.
	if _, _, _, _, ok := voteOnLine("15:04 Fan▲99 • #42:abcd"); ok {
		t.Fatal("pre-meta arrows must not match")
	}
	if height, _, _, _, ok := voteOnLine("15:04 Fan▲99 • #42:abcd ▲0 ▼0"); !ok || height != 42 {
		t.Fatal("tokens after the reference must still map")
	}
	if _, _, _, _, ok := voteOnLine("15:04 Alice • #42:abcd"); ok {
		t.Fatal("meta without tokens must not match")
	}
	if _, _, _, _, ok := voteOnLine("plain ▲5 line without meta"); ok {
		t.Fatal("tokens without meta must not match")
	}
	if _, _, _, _, ok := voteOnLine("plain line without anything"); ok {
		t.Fatal("plain line must not match")
	}
}

func TestAppVoteTokenClick(t *testing.T) {
	sess, _ := behaviorSession(t)
	seedIndexedWire(sess, 42)
	stub := &stubConn{}
	sess.Conn = stub
	sess.Display.TabChat.append(entry{
		header: "15:04 Alice • #42:abcd ▲0 ▼0",
		height: 42, hashHex: "abcd",
	})
	a := newApp(sess)
	a.width, a.height = 80, 24
	a.layout()
	a.dirty = true
	a.refreshContent()
	if len(a.votes) != 2 {
		t.Fatalf("up and down tokens must expose two spans, got %d", len(a.votes))
	}
	click := func(i int, direction int) {
		span := a.votes[i]
		if span.direction != direction {
			t.Fatalf("span %d direction = %d, want %d", i, span.direction, direction)
		}
		y := span.row - a.viewport.YOffset() + chatTop
		stub.wrote = nil
		_, cmd := a.Update(tea.MouseClickMsg{X: span.start, Y: y, Button: tea.MouseLeft})
		if cmd == nil {
			t.Fatalf("a click on span %d must vote", i)
		}
		cmd()
		req, ok := stub.wrote.(VoteRequest)
		if !ok || req.Type != "vote" || req.Target != 42 || req.Direction != direction {
			t.Fatalf("span %d wrote = %#v", i, stub.wrote)
		}
	}
	click(0, wire.DirUpvote)
	click(1, wire.DirDownvote)

	// Left of a token it stays a plain selection.
	span := a.votes[0]
	y := span.row - a.viewport.YOffset() + chatTop
	model, cmd := a.Update(tea.MouseClickMsg{X: span.start - 1, Y: y, Button: tea.MouseLeft})
	if cmd != nil {
		t.Fatal("a click left of a token must not vote")
	}
	if next, ok := model.(*app); !ok || !next.selActive {
		t.Fatal("a click off the tokens must still start a selection")
	}
}

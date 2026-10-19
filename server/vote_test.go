package main

import (
	"encoding/json"
	"testing"

	"github.com/CleveTok3125/V2V/lib/config"
	"github.com/CleveTok3125/V2V/lib/wire"
)

// applyVote keeps totals by height only: repeating the held direction
// toggles it back off, an opposite direction flips it.
func TestApplyVoteToggle(t *testing.T) {
	s := NewChatServer()
	upvotes, downvotes, dup, changed := s.applyVote("1.1.1.1", 5, wire.DirUpvote)
	if dup || !changed || upvotes != 1 || downvotes != 0 {
		t.Fatalf("first upvote = %d/%d dup=%v changed=%v", upvotes, downvotes, dup, changed)
	}
	upvotes, downvotes, dup, changed = s.applyVote("1.1.1.1", 5, wire.DirUpvote)
	if dup || !changed || upvotes != 0 || downvotes != 0 {
		t.Fatalf("repeat upvote must toggle off = %d/%d dup=%v changed=%v", upvotes, downvotes, dup, changed)
	}
	upvotes, downvotes, dup, changed = s.applyVote("1.1.1.1", 5, wire.DirDownvote)
	if dup || !changed || upvotes != 0 || downvotes != 1 {
		t.Fatalf("downvote after withdraw = %d/%d dup=%v changed=%v", upvotes, downvotes, dup, changed)
	}
	upvotes, downvotes, dup, changed = s.applyVote("1.1.1.1", 5, wire.DirUpvote)
	if dup || !changed || upvotes != 1 || downvotes != 0 {
		t.Fatalf("flip = %d/%d dup=%v changed=%v", upvotes, downvotes, dup, changed)
	}
	upvotes, downvotes, dup, changed = s.applyVote("1.1.1.1", 5, wire.DirWithdraw)
	if dup || !changed || upvotes != 0 || downvotes != 0 {
		t.Fatalf("withdraw = %d/%d dup=%v changed=%v", upvotes, downvotes, dup, changed)
	}
	if _, _, _, changed := s.applyVote("1.1.1.1", 5, wire.DirWithdraw); changed {
		t.Fatal("withdraw with nothing held must not change")
	}
	if _, _, dup, changed := s.applyVote("1.1.1.1", 5, wire.DirWithdraw); !dup || changed {
		t.Fatalf("repeat withdraw must report dup without change, dup=%v changed=%v", dup, changed)
	}
	if _, _, dup, _ := s.applyVote("2.2.2.2", 6, wire.DirWithdraw); dup {
		t.Fatal("first withdraw must not report dup")
	}
}

// Totals aggregate across addresses and may go negative.
func TestApplyVoteNegativeAcrossIPs(t *testing.T) {
	s := NewChatServer()
	s.applyVote("1.1.1.1", 9, wire.DirDownvote)
	s.applyVote("2.2.2.2", 9, wire.DirDownvote)
	upvotes, downvotes, _, _ := s.applyVote("3.3.3.3", 9, wire.DirUpvote)
	if upvotes != 1 || downvotes != 2 {
		t.Fatalf("totals = %d/%d, want 1/2", upvotes, downvotes)
	}
	if upvotes-downvotes != -1 {
		t.Fatalf("score = %d, want -1", upvotes-downvotes)
	}
}

// BroadcastVote reuses the notice path: unchained, sequenced, tagged.
func TestBroadcastVoteWire(t *testing.T) {
	testCfg(t)
	s := NewChatServer()
	s.Hub.BroadcastVote(7, 12, 3)
	if len(s.Chain.History) != 1 {
		t.Fatalf("history len = %d, want 1", len(s.Chain.History))
	}
	var got WireMessage
	if err := json.Unmarshal([]byte(s.Chain.History[0]), &got); err != nil {
		t.Fatal(err)
	}
	if got.Type != "system" || got.VoteTarget != 7 || got.VoteUpvotes != 12 || got.VoteDownvotes != 3 {
		t.Fatalf("vote wire = %+v", got)
	}
	if !wire.HasTag(got.Tags, wire.TagVote) {
		t.Fatalf("vote wire tags = %v, want system.vote", got.Tags)
	}
	if got.ChainHeight != 0 || got.ChainHash != "" {
		t.Fatalf("vote must stay unchained: %+v", got)
	}
	if got.Seq == 0 {
		t.Fatal("vote must carry a history seq")
	}
}

// Rebuild replays stored vote lines: last cumulative per target wins.
func TestRebuildVoteScores(t *testing.T) {
	testCfg(t)
	s := NewChatServer()
	s.Hub.BroadcastVote(4, 2, 0)
	s.Hub.BroadcastVote(4, 2, 1)
	s.Hub.BroadcastVote(8, 0, 5)
	fresh := NewChatServer()
	fresh.Chain.History = append([]string(nil), s.Chain.History...)
	fresh.rebuildVoteScores()
	fresh.VoteMu.Lock()
	defer fresh.VoteMu.Unlock()
	if tot := fresh.VoteScores[4]; tot == nil || tot.Upvotes != 2 || tot.Downvotes != 1 {
		t.Fatalf("height 4 = %+v, want 2/1", tot)
	}
	if tot := fresh.VoteScores[8]; tot == nil || tot.Upvotes != 0 || tot.Downvotes != 5 {
		t.Fatalf("height 8 = %+v, want 0/5", tot)
	}
}

// handleVote rejects bad directions and future heights without storing.
func TestHandleVoteRejects(t *testing.T) {
	testCfg(t)
	s := NewChatServer()
	sess := &ClientSession{Send: make(chan []byte, 16)}
	s.handleVote(sess, "9.9.9.9", 1, 5)
	s.Chain.Mu.Lock()
	s.Chain.ready, s.Chain.height = true, 3
	s.Chain.Mu.Unlock()
	s.handleVote(sess, "9.9.9.9", 99, 1)
	if len(s.Chain.History) != 0 {
		t.Fatalf("rejected votes must not store, history = %d", len(s.Chain.History))
	}
}

// handleVote toggles a repeat vote back off and broadcasts both totals.
func TestHandleVoteToggleOff(t *testing.T) {
	testCfg(t)
	dyn := config.DefaultDynamic()
	dyn.MessageCooldown = 0
	Cfg.Dynamic.Store(dyn)
	s := NewChatServer()
	sess := &ClientSession{Send: make(chan []byte, 16)}
	s.handleVote(sess, "7.7.7.7", 1, 1)
	s.handleVote(sess, "7.7.7.7", 1, 1)
	if len(s.Chain.History) != 2 {
		t.Fatalf("toggle must store, history = %d", len(s.Chain.History))
	}
	s.VoteMu.Lock()
	tot := s.VoteScores[1]
	s.VoteMu.Unlock()
	if tot == nil || tot.Upvotes != 0 || tot.Downvotes != 0 {
		t.Fatalf("totals = %+v, want 0/0", tot)
	}
}

// handleVote throttles rapid votes from one address at message cadence.
func TestHandleVoteCooldown(t *testing.T) {
	testCfg(t)
	s := NewChatServer()
	sess := &ClientSession{Send: make(chan []byte, 16)}
	s.handleVote(sess, "6.6.6.6", 1, wire.DirUpvote)
	s.handleVote(sess, "6.6.6.6", 2, wire.DirUpvote)
	if len(s.Chain.History) != 1 {
		t.Fatalf("second rapid vote must not store, history = %d", len(s.Chain.History))
	}
	s.handleVote(sess, "7.7.7.6", 2, wire.DirUpvote)
	if len(s.Chain.History) != 2 {
		t.Fatalf("another address must still vote, history = %d", len(s.Chain.History))
	}
}

// VoteRequest rides the envelope type switch like history_request. The
// direction keeps its short key on the wire.
func TestVoteRequestRoundTrip(t *testing.T) {
	raw, _ := json.Marshal(wire.VoteRequest{Type: "vote", Target: 11, Direction: wire.DirDownvote})
	var back wire.VoteRequest
	if err := json.Unmarshal(raw, &back); err != nil || back.Target != 11 || back.Direction != wire.DirDownvote {
		t.Fatalf("round trip = %+v %v", back, err)
	}
	var keys map[string]any
	if err := json.Unmarshal(raw, &keys); err != nil {
		t.Fatal(err)
	}
	if _, ok := keys["dir"]; !ok {
		t.Fatalf("direction must serialize as dir, got %v", keys)
	}
}

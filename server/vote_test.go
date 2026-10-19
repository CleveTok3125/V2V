package main

import (
	"encoding/json"
	"testing"

	"github.com/CleveTok3125/V2V/lib/wire"
)

// applyVote keeps totals by height only: repeats of the same direction
// from one address report dup, an opposite direction flips.
func TestApplyVoteDupAndFlip(t *testing.T) {
	s := NewChatServer()
	up, down, dup, changed := s.applyVote("1.1.1.1", 5, 1)
	if dup || !changed || up != 1 || down != 0 {
		t.Fatalf("first up = %d/%d dup=%v changed=%v", up, down, dup, changed)
	}
	if _, _, dup, _ := s.applyVote("1.1.1.1", 5, 1); !dup {
		t.Fatal("same direction repeat must report dup")
	}
	up, down, dup, changed = s.applyVote("1.1.1.1", 5, -1)
	if dup || !changed || up != 0 || down != 1 {
		t.Fatalf("flip = %d/%d dup=%v changed=%v", up, down, dup, changed)
	}
	up, down, dup, changed = s.applyVote("1.1.1.1", 5, 0)
	if dup || !changed || up != 0 || down != 0 {
		t.Fatalf("withdraw = %d/%d dup=%v changed=%v", up, down, dup, changed)
	}
	if _, _, _, changed := s.applyVote("1.1.1.1", 5, 0); changed {
		t.Fatal("withdraw with nothing held must not change")
	}
}

// Totals aggregate across addresses and may go negative.
func TestApplyVoteNegativeAcrossIPs(t *testing.T) {
	s := NewChatServer()
	s.applyVote("1.1.1.1", 9, -1)
	s.applyVote("2.2.2.2", 9, -1)
	up, down, _, _ := s.applyVote("3.3.3.3", 9, 1)
	if up != 1 || down != 2 {
		t.Fatalf("totals = %d/%d, want 1/2", up, down)
	}
	if up-down != -1 {
		t.Fatalf("score = %d, want -1", up-down)
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
	if got.Type != "system" || got.VoteTarget != 7 || got.VoteUp != 12 || got.VoteDown != 3 {
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
	if tot := fresh.VoteScores[4]; tot == nil || tot.Up != 2 || tot.Down != 1 {
		t.Fatalf("height 4 = %+v, want 2/1", tot)
	}
	if tot := fresh.VoteScores[8]; tot == nil || tot.Up != 0 || tot.Down != 5 {
		t.Fatalf("height 8 = %+v, want 0/5", tot)
	}
}

// handleVote rejects bad dirs and future heights without storing.
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

// handleVote stores once then rejects the repeat as duplicate.
func TestHandleVoteOkThenDup(t *testing.T) {
	testCfg(t)
	s := NewChatServer()
	sess := &ClientSession{Send: make(chan []byte, 16)}
	s.handleVote(sess, "7.7.7.7", 1, 1)
	if len(s.Chain.History) != 1 {
		t.Fatalf("history = %d, want 1", len(s.Chain.History))
	}
	s.handleVote(sess, "7.7.7.7", 1, 1)
	if len(s.Chain.History) != 1 {
		t.Fatalf("dup must not store, history = %d", len(s.Chain.History))
	}
	s.VoteMu.Lock()
	tot := s.VoteScores[1]
	s.VoteMu.Unlock()
	if tot == nil || tot.Up != 1 || tot.Down != 0 {
		t.Fatalf("totals = %+v, want 1/0", tot)
	}
}

// VoteRequest rides the envelope type switch like history_request.
func TestVoteRequestRoundTrip(t *testing.T) {
	raw, _ := json.Marshal(wire.VoteRequest{Type: "vote", Target: 11, Dir: -1})
	var back wire.VoteRequest
	if err := json.Unmarshal(raw, &back); err != nil || back.Target != 11 || back.Dir != -1 {
		t.Fatalf("round trip = %+v %v", back, err)
	}
}

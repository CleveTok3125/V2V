package main

import (
	"fmt"
	"strings"
)

// applyVoteLocked folds one vote wire into the score map and repaints
// its target block. Reports whether msg was a vote wire. Caller holds
// DisplayMu.
func (s *Session) applyVoteLocked(msg WireMessage) bool {
	if !isVoteWire(msg) {
		return false
	}
	if s.Chain.Votes == nil {
		s.Chain.Votes = make(map[uint64]VoteScore)
	}
	if msg.VoteUp == 0 && msg.VoteDown == 0 {
		delete(s.Chain.Votes, msg.VoteTarget)
	} else {
		s.Chain.Votes[msg.VoteTarget] = VoteScore{Up: msg.VoteUp, Down: msg.VoteDown}
	}
	s.refreshVoteLocked(msg.VoteTarget)
	return true
}

// refreshVoteLocked rebuilds the chat block for one voted height so its
// header carries the new score. Evicted targets keep their score in the
// map for /info. Caller holds DisplayMu.
func (s *Session) refreshVoteLocked(target uint64) {
	if s.Chain.WireIdx == nil {
		return
	}
	wire, ok := s.Chain.WireIdx.get(target)
	if !ok {
		return
	}
	idx := entryIndexByHeight(s.Display.TabChat.lines, target, "")
	if idx < 0 {
		return
	}
	s.Verify.AutoVerifyMu.RLock()
	av := s.Verify.AutoVerify
	s.Verify.AutoVerifyMu.RUnlock()
	s.Display.ShowMetaMu.RLock()
	withMeta := s.Display.ShowMeta
	s.Display.ShowMetaMu.RUnlock()
	full, _ := s.buildChatBlock(wire, av, withMeta)
	full = maybeCollapse(full, wire, TabChat)
	s.Display.TabChat.spliceOut(idx, idx+1)
	s.Display.TabChat.insertAt(idx, []entry{full})
	if s.Display.Sink != nil {
		s.Display.Sink.Notify()
	}
}

// cmdVote sends /up, /down or /unvote for one buffered chat message.
// Votes ride sendJSON as VoteRequest; the server broadcasts totals.
func (s *Session) cmdVote(text string) bool {
	var dir int
	var rest string
	switch {
	case text == "/up" || strings.HasPrefix(text, "/up "):
		dir = 1
		rest = strings.TrimSpace(strings.TrimPrefix(text, "/up"))
	case text == "/down" || strings.HasPrefix(text, "/down "):
		dir = -1
		rest = strings.TrimSpace(strings.TrimPrefix(text, "/down"))
	case text == "/unvote" || strings.HasPrefix(text, "/unvote "):
		dir = 0
		rest = strings.TrimSpace(strings.TrimPrefix(text, "/unvote"))
	default:
		return false
	}
	height, suffix, err := parseFindArg(rest)
	if err != nil {
		s.Display.DisplayMu.Lock()
		s.emitLocalFeedback("[Local]: Dùng /up <height>[:hash] | /down <height>[:hash] | /unvote <height>[:hash].\n")
		s.Display.DisplayMu.Unlock()
		s.refreshTerminal()
		return true
	}
	s.Display.DisplayMu.Lock()
	wire, ok := s.Chain.WireIdx.get(height)
	if !ok || !quotable(wire) || entryIndexByHeight(s.Display.TabChat.lines, height, suffix) < 0 {
		s.emitLocalFeedback(fmt.Sprintf("[Local]: Tin #%d không còn trong bộ nhớ, không vote được.\n", height))
		s.Display.DisplayMu.Unlock()
		s.refreshTerminal()
		return true
	}
	s.Display.DisplayMu.Unlock()
	if err := s.sendJSON(VoteRequest{Type: "vote", Target: height, Dir: dir}); err != nil {
		s.Display.DisplayMu.Lock()
		s.emitLocalFeedback(fmt.Sprintf("[Local]: Không gửi được vote: %v.\n", err))
		s.Display.DisplayMu.Unlock()
		s.refreshTerminal()
	}
	return true
}

package main

import (
	"fmt"
	"strings"

	"github.com/CleveTok3125/V2V/lib/wire"
)

// voteStripFor renders the score strip for one height: "▲12 ▼3".
//
// Zero counts still draw, so every message shows its score and stays
// clickable.
//
// Caller holds DisplayMu.
func (s *Session) voteStripFor(height uint64) string {
	if height == 0 {
		return ""
	}
	var count VoteScore
	if s.Chain.Votes != nil {
		count = s.Chain.Votes[height]
	}
	return upvoteStyle.Render(fmt.Sprintf("▲%d", count.Upvotes)) +
		" " + warnStyle.Render(fmt.Sprintf("▼%d", count.Downvotes))
}

// applyVoteLocked folds one vote wire into the score map and repaints
// its target block.
//
// Reports whether msg was a vote wire.
//
// Caller holds DisplayMu.
func (s *Session) applyVoteLocked(msg WireMessage) bool {
	if !isVoteWire(msg) {
		return false
	}
	if s.Chain.Votes == nil {
		s.Chain.Votes = make(map[uint64]VoteScore)
	}
	if msg.VoteUpvotes == 0 && msg.VoteDownvotes == 0 {
		delete(s.Chain.Votes, msg.VoteTarget)
	} else {
		s.Chain.Votes[msg.VoteTarget] = VoteScore{Upvotes: msg.VoteUpvotes, Downvotes: msg.VoteDownvotes}
	}
	s.refreshVoteLocked(msg.VoteTarget)
	return true
}

// refreshVoteLocked rebuilds the chat block for one voted height so its
// header carries the new score.
//
// Evicted targets keep their score in the map for /info.
//
// Caller holds DisplayMu.
func (s *Session) refreshVoteLocked(target uint64) {
	if s.Chain.WireIdx == nil {
		return
	}
	msg, ok := s.Chain.WireIdx.get(target)
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
	full, _ := s.buildChatBlock(msg, av, withMeta)
	full = maybeCollapse(full, msg, TabChat)
	s.Display.TabChat.spliceOut(idx, idx+1)
	s.Display.TabChat.insertAt(idx, []entry{full})
	if s.Display.Sink != nil {
		s.Display.Sink.Notify()
	}
}

// sendVote validates height against the buffer and casts a vote in
// direction.
//
// Suffix checks the hash checksum when given. Reports whether the vote
// was sent.
func (s *Session) sendVote(height uint64, suffix string, direction int) bool {
	s.Display.DisplayMu.Lock()
	msg, ok := s.Chain.WireIdx.get(height)
	if !ok || !quotable(msg) || entryIndexByHeight(s.Display.TabChat.lines, height, suffix) < 0 {
		s.emitLocalFeedback(fmt.Sprintf("[Local]: Tin #%d không còn trong bộ nhớ, không vote được.\n", height))
		s.Display.DisplayMu.Unlock()
		s.refreshTerminal()
		return false
	}
	s.Display.DisplayMu.Unlock()
	if err := s.sendJSON(VoteRequest{Type: "vote", Target: height, Direction: direction}); err != nil {
		s.Display.DisplayMu.Lock()
		s.emitLocalFeedback(fmt.Sprintf("[Local]: Không gửi được vote: %v.\n", err))
		s.Display.DisplayMu.Unlock()
		s.refreshTerminal()
		return false
	}
	return true
}

// cmdVote sends /upvote, /downvote or /unvote for one buffered chat
// message.
//
// /up and /down are short aliases.
func (s *Session) cmdVote(text string) bool {
	var direction int
	var rest string
	switch {
	case text == "/upvote" || strings.HasPrefix(text, "/upvote "):
		direction = wire.DirUpvote
		rest = strings.TrimSpace(strings.TrimPrefix(text, "/upvote"))
	case text == "/up" || strings.HasPrefix(text, "/up "):
		direction = wire.DirUpvote
		rest = strings.TrimSpace(strings.TrimPrefix(text, "/up"))
	case text == "/downvote" || strings.HasPrefix(text, "/downvote "):
		direction = wire.DirDownvote
		rest = strings.TrimSpace(strings.TrimPrefix(text, "/downvote"))
	case text == "/down" || strings.HasPrefix(text, "/down "):
		direction = wire.DirDownvote
		rest = strings.TrimSpace(strings.TrimPrefix(text, "/down"))
	case text == "/unvote" || strings.HasPrefix(text, "/unvote "):
		direction = wire.DirWithdraw
		rest = strings.TrimSpace(strings.TrimPrefix(text, "/unvote"))
	default:
		return false
	}
	height, suffix, err := parseFindArg(rest)
	if err != nil {
		s.Display.DisplayMu.Lock()
		s.emitLocalFeedback("[Local]: Dùng /upvote <height>[:hash] | /downvote <height>[:hash] | /unvote <height>[:hash] (/up, /down là alias).\n")
		s.Display.DisplayMu.Unlock()
		s.refreshTerminal()
		return true
	}
	s.sendVote(height, suffix, direction)
	return true
}

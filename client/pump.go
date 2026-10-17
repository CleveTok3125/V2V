package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/CleveTok3125/V2V/lib/filter"
	"github.com/CleveTok3125/V2V/lib/wire"
)

// Session read pump and async verify worker (moved from main).

// catchupHoldMax bounds how long a join-replay catch-up may defer
// output while waiting for its refill: past it the held stream prints
// with whatever arrived, so a lost recovery reply cannot blank the
// session indefinitely.
const catchupHoldMax = 5 * time.Second

// greetingGrace releases the held welcome line when no join replay
// arrives shortly after connect.
const greetingGrace = 400 * time.Millisecond

// flushDateBannerLocked prints a stashed date banner to TabSystem
// before the block that follows it. A live banner during an older
// window appends; a history banner splices. Caller must hold DisplayMu.
func (s *Session) flushDateBannerLocked() {
	if s.Pending.PendingDateBannerWire != nil {
		msg := s.Pending.PendingDateBannerWire
		s.Pending.PendingDateBannerWire = nil
		if s.showDateBannerLocked(msg.SysDate) {
			wasOlder := s.Chain.InOlder
			if wasOlder && !s.isOlderHistoryWire(*msg) {
				s.Chain.InOlder = false
				s.renderChatBlock(*msg)
				s.Chain.InOlder = wasOlder
				return
			}
			s.renderChatBlock(*msg)
		}
	}
}

// showDateBannerLocked records the day a banner announces and reports whether it
// is worth drawing. A server that restarts re-announces the current day,
// which would otherwise print a second identical banner right after the
// replay carried one. The comparison is on sys_date, the machine value the
// server stamps, so reformatting the banner text cannot defeat it. A banner
// with no sys_date has nothing to compare, so it always prints and nothing
// is recorded.
func (s *Session) showDateBannerLocked(day string) bool {
	if day == "" {
		return true
	}
	if day == s.Display.LastDateBanner {
		return false
	}
	s.Display.LastDateBanner = day
	return true
}

// emitRawLineLocked prints a frame that is not a wire. Since notices are
// tagged, what still arrives raw is a replay marker or a line from a peer
// that predates tagging; the marker branches above handle the first, and
// the second is undiagnosable without reading its text, so it lands in
// TabChat ungated rather than being classified by wording.
// Caller must hold DisplayMu.
func (s *Session) emitRawLineLocked(line string) {
	s.emitTabLive(true, TabChat, systemEntry(filter.SanitizeForDisplay(line)))
}

// trackReplayWindow updates the replay window flags for one boundary
// line from an older server. New servers send no markers: windows are
// tracked from the request (requestOlder, handleHistoryInfo,
// sendRecoverLocked) until their history_sync trailer. Kept so an old
// server still drives the same state machine.
func (s *Session) trackReplayWindow(tags []string, start bool) {
	if start {
		switch {
		case wire.HasTag(tags, wire.TagHistoryOlder):
			s.Chain.InOlder = true
			s.Chain.OlderInsert = olderAnchor(s.Display.TabChat)
			s.Chain.OlderInsertSys = olderAnchor(s.Display.TabSys)
		case wire.HasTag(tags, wire.TagHistoryRecover):
			s.Chain.InRecover = true
		default:
			s.Chain.InSync = true
		}
		return
	}
	s.flushDateBannerLocked()
	if wire.HasTag(tags, wire.TagHistoryOlder) && wire.HasTag(tags, wire.TagHistoryExhausted) {
		// Nothing below the oldest known height: stop asking.
		s.Chain.OlderExhausted = true
	}
	s.Chain.InOlder = false
	s.Chain.InRecover = false
	s.Pending.PendingDateBannerWire = nil
	s.Chain.InSync = false
}

// handleReplayMarker applies one replay window marker from an older
// server. Markers never render as chat lines: progress and results live
// in the status bar from the trailer. Only the state machine runs.
func (s *Session) handleReplayMarker(msg WireMessage) {
	tags := msg.Tags
	boundary, start := parseHistoryBoundary(tags)
	if !boundary {
		// Without the history tag a line is an ordinary notice, and treating
		// it as a header would raise InSync on whatever text it carried.
		return
	}

	s.Display.DisplayMu.Lock()
	s.trackReplayWindow(tags, start)
	// A recovery footer settles its refill after the footer line, so the
	// confirmation reads as a result of the window instead of preceding its
	// own footer.
	if !start && wire.HasTag(tags, wire.TagHistoryRecover) {
		s.finishRecovery()
	}
	s.Display.DisplayMu.Unlock()
}

// handleHistoryInfo starts the client-driven initial load: the server
// announces the available window, the client holds live output and
// pages ascending until it has initialLines or the server reports no
// more. Progress shows in the status bar. Caller refreshes after.
func (s *Session) handleHistoryInfo(info HistoryInfo) {
	s.Display.DisplayMu.Lock()
	defer s.Display.DisplayMu.Unlock()
	if s.Chain.Loading {
		return
	}
	target := 500
	if ClientCfg != nil {
		target = ClientCfg.HistoryInitialLines()
	}
	s.Chain.Loading = true
	s.Chain.InSync = true
	s.Chain.SyncHeights = map[uint64][32]byte{}
	s.Chain.LoadTarget = target
	s.Chain.LoadLoaded = 0
	s.Chain.LoadRetried = 0
	s.Chain.LoadHold = nil
	s.Chain.LoadMaxSeq = info.MaxSeq
	// after_seq is exclusive: to load the last `target` lines ending at
	// MaxSeq, start just below MaxSeq-target+1.
	after := uint64(0)
	if info.MaxSeq >= uint64(target) {
		after = info.MaxSeq - uint64(target)
	}
	s.Chain.LoadStartSeq = after + 1
	// Hold live output until the load completes so the history and the
	// live stream print as one continuous block.
	s.Display.CatchupHold = true
	s.Display.HoldGen++
	gen := s.Display.HoldGen
	s.Display.HoldTimer = time.AfterFunc(catchupHoldMax, func() {
		s.Display.DisplayMu.Lock()
		if s.Display.HoldGen == gen && s.Chain.Loading {
			s.finishLoadLocked()
		}
		s.Display.DisplayMu.Unlock()
	})
	s.sendLoadPageLocked(after)
}

// sendLoadPageLocked requests one ascending page from cursor. Caller
// must hold DisplayMu.
func (s *Session) sendLoadPageLocked(cursor uint64) {
	if s.Conn == nil {
		s.finishLoadLocked()
		return
	}
	batch := 100
	if ClientCfg != nil {
		batch = ClientCfg.HistoryBatchLines()
	}
	after := cursor
	if err := s.sendJSON(HistoryRequest{Type: "history_request", AfterSeq: &after, Limit: batch}); err != nil {
		s.finishLoadLocked()
	}
}

// onLoadPageLocked consumes one load-page trailer: retry a page that
// lost lines, then page on while the target and the announced window
// both have more, otherwise finish. Caller must hold DisplayMu.
func (s *Session) onLoadPageLocked(hs HistorySync) {
	if hs.Dropped > 0 {
		retries := 2
		if ClientCfg != nil {
			retries = ClientCfg.HistoryRecoverRetries()
		}
		if s.Chain.LoadRetried < retries {
			s.Chain.LoadRetried++
			after := uint64(0)
			if hs.FirstSeq > 0 {
				after = hs.FirstSeq - 1
			}
			s.sendLoadPageLocked(after)
			return
		}
	}
	s.Chain.LoadRetried = 0
	if hs.More && s.Chain.LoadLoaded < s.Chain.LoadTarget && hs.NextSeq < s.Chain.LoadMaxSeq {
		s.sendLoadPageLocked(hs.NextSeq)
		return
	}
	s.finishLoadLocked()
}

// finishLoadLocked ends the initial load: flush held live lines after
// all history so they land at the bottom in arrival order, release the
// hold, show the result in the status bar, and run the fork check.
// Caller must hold DisplayMu.
func (s *Session) finishLoadLocked() {
	loaded := s.Chain.LoadLoaded
	s.Chain.Loading = false
	s.Chain.InSync = false
	// Held lines are live-only (newer than the window, or seq-less):
	// append them now that history is complete, bypassing any older
	// window still in flight (they are live, never older history).
	if len(s.Chain.LoadHold) > 0 {
		s.Display.ShowJoinMu.RLock()
		isShowingJoin := s.Display.ShowJoinLeave
		s.Display.ShowJoinMu.RUnlock()
		wasOlder := s.Chain.InOlder
		s.Chain.InOlder = false
		for _, h := range s.Chain.LoadHold {
			if h.isRaw {
				s.emitRawLineLocked(h.raw)
				continue
			}
			msg := h.msg
			if msg.Type == "chat" {
				if rendered := s.verifyReplayWire(msg, true); !rendered {
					s.flushDateBannerLocked()
					s.renderChatBlock(msg)
				}
				continue
			}
			if msg.Type != "system" {
				continue
			}
			rendered := s.verifyReplayWire(msg, false)
			if !rendered && !isShowingJoin && wire.HasTag(msg.Tags, wire.TagDate) {
				s.Pending.PendingDateBannerWire = &msg
				continue
			}
			if !rendered && !isShowingJoin && wire.HasAnyTag(msg.Tags, wire.TagJoin, wire.TagLeave) {
				continue
			}
			if !rendered {
				s.flushDateBannerLocked()
				s.renderChatBlock(msg)
			}
		}
		s.Chain.InOlder = wasOlder
		s.Chain.LoadHold = nil
	}
	if loaded > 0 {
		s.setStatusLocked(fmt.Sprintf("Đã tải %d tin", loaded))
	} else {
		s.setStatusLocked("Không có lịch sử")
	}
	s.releaseCatchupLocked()
	if warn, flush := forkWarning(HistorySync{Type: "history_sync", MaxHeight: ^uint64(0)},
		s.Chain.HavePersistedTip, s.Chain.PersistedTip, s.Chain.PersistedHeight, s.Chain.SyncHeights); warn != "" {
		s.emitLocalFeedback(warn)
		if flush {
			s.flushChainTip()
		}
	}
	s.Chain.SyncHeights = map[uint64][32]byte{}
}

// handleHistorySync consumes a replay trailer from either a whole
// frame or a coalesced per-line blob. A load-page trailer continues the
// initial load; an older trailer settles its segment and shows the
// result in the status bar; a recovery trailer settles its refill.
// Caller refreshes after.
func (s *Session) handleHistorySync(hs HistorySync) {
	s.Display.DisplayMu.Lock()
	if s.Chain.Loading && hs.Direction == "after" {
		s.onLoadPageLocked(hs)
		s.Display.DisplayMu.Unlock()
		return
	}
	if s.Chain.InOlder {
		s.Chain.InOlder = false
		s.Chain.OlderHeightBound = 0
		s.Chain.OlderSeqBound = 0
		s.flushDateBannerLocked()
		s.Pending.PendingDateBannerWire = nil
		if hs.Total == 0 {
			// Nothing below the oldest known height: stop asking.
			s.Chain.OlderExhausted = true
			s.setStatusLocked("Không còn tin cũ hơn")
		} else {
			s.setStatusLocked(fmt.Sprintf("Đã tải %d tin cũ", hs.Sent))
		}
		if s.Chain.RecoverPending != nil {
			s.finishRecovery()
		}
		s.Display.DisplayMu.Unlock()
		return
	}
	if s.Chain.InRecover || s.Chain.RecoverPending != nil || hs.Direction == "recover" {
		s.Chain.InRecover = false
		s.finishRecovery()
		s.Display.DisplayMu.Unlock()
		return
	}
	s.Display.DisplayMu.Unlock()
}

// runPump reads server frames (chat/system/history/trailer) and
// renders them. Started once from main.
func (s *Session) runPump() {
	if s.PumpDone != nil {
		defer close(s.PumpDone)
	}
	// Release the held welcome line when no join replay follows it (an
	// empty or replay-less server), so it never waits forever.
	time.AfterFunc(greetingGrace, func() {
		s.Display.DisplayMu.Lock()
		s.flushPendingGreetingLocked()
		s.Display.DisplayMu.Unlock()
	})

	for {
		_, msg, err := s.Conn.ReadMessage()
		if err != nil {
			select {
			case <-s.Quitting:
				return
			default:
				// A held catch-up must land before the exit path so its
				// buffered history is not lost with the process.
				s.Display.DisplayMu.Lock()
				s.releaseCatchupLocked()
				s.flushPendingGreetingLocked()
				s.emitLocalFeedbackLive(true, "[Local]: ❌ Mất kết nối server\n")
				s.Display.DisplayMu.Unlock()
				// Give the app a moment to paint the notice before the
				// process exits (os.Exit skips the app's teardown).
				time.Sleep(250 * time.Millisecond)
				s.flushChainTip()
				s.Display.Term.Close()
				ClearLoadedPassphrase()
				os.Exit(1)
			}
		}

		s.Display.ShowJoinMu.RLock()
		isShowingJoin := s.Display.ShowJoinLeave
		s.Display.ShowJoinMu.RUnlock()

		// Try to handle structured WireMessage JSON first (for new protocol)
		var chatWire WireMessage
		if err := json.Unmarshal(msg, &chatWire); err == nil && chatWire.Type == "chat" {
			s.Display.DisplayMu.Lock()
			if s.Chain.Loading && s.Chain.InSync && !s.isLoadHistoryWire(chatWire) {
				s.Chain.LoadHold = append(s.Chain.LoadHold, heldLine{msg: chatWire})
				s.Display.DisplayMu.Unlock()
				s.refreshCoalesced()
				continue
			}
			wasOlder := s.Chain.InOlder
			liveOlder := wasOlder && !s.isOlderHistoryWire(chatWire)
			if liveOlder {
				s.flushDateBannerLocked()
				s.Chain.InOlder = false
			}
			if rendered := s.verifyReplayWire(chatWire, true); !rendered {
				if !liveOlder {
					s.flushDateBannerLocked()
				}
				s.renderChatBlock(chatWire)
			}
			s.Chain.InOlder = wasOlder
			s.Display.DisplayMu.Unlock()
			s.refreshCoalesced()
			continue
		}
		var sysWire WireMessage
		if err := json.Unmarshal(msg, &sysWire); err == nil && sysWire.Type == "system" {
			if wire.HasTag(sysWire.Tags, wire.TagHistory) {
				s.handleReplayMarker(sysWire)
				s.refreshCoalesced()
				continue
			}
			s.Display.DisplayMu.Lock()
			if s.Chain.Loading && s.Chain.InSync && !s.isLoadHistoryWire(sysWire) {
				s.Chain.LoadHold = append(s.Chain.LoadHold, heldLine{msg: sysWire})
				s.Display.DisplayMu.Unlock()
				s.refreshCoalesced()
				continue
			}
			wasOlder := s.Chain.InOlder
			liveOlder := wasOlder && !s.isOlderHistoryWire(sysWire)
			if liveOlder {
				s.flushDateBannerLocked()
				s.Chain.InOlder = false
			}
			rendered := s.verifyReplayWire(sysWire, false)
			if !rendered && !isShowingJoin && wire.HasTag(sysWire.Tags, wire.TagDate) && !liveOlder {
				s.Pending.PendingDateBannerWire = &sysWire
				s.Chain.InOlder = wasOlder
				s.Display.DisplayMu.Unlock()
				s.refreshCoalesced()
				continue
			}
			if !rendered && !isShowingJoin && wire.HasAnyTag(sysWire.Tags, wire.TagJoin, wire.TagLeave) {
				s.Chain.InOlder = wasOlder
				s.Display.DisplayMu.Unlock()
				s.refreshCoalesced()
				continue
			}
			if !rendered {
				if !liveOlder {
					s.flushDateBannerLocked()
				}
				s.renderChatBlock(sysWire)
			}
			s.Chain.InOlder = wasOlder
			s.Display.DisplayMu.Unlock()
			s.refreshCoalesced()
			continue
		}
		// Machine-readable replay trailer: never rendered, only the
		// fork check below consumes it.
		if hs, ok := parseHistorySync(msg); ok {
			s.handleHistorySync(hs)
			s.refreshCoalesced()
			continue
		}
		// History window announcement: starts the client-driven load.
		var info HistoryInfo
		if err := json.Unmarshal(msg, &info); err == nil && info.Type == "history_info" {
			s.handleHistoryInfo(info)
			s.refreshCoalesced()
			continue
		}
		// In-chat PoW offer: verify and solve in background so the
		// pump never blocks on proof-of-work.
		var offer PowOffer
		if err := json.Unmarshal(msg, &offer); err == nil && offer.Type == "pow_offer" {
			go s.handlePowOffer(offer)
			s.refreshCoalesced()
			continue
		}
		for _, line := range strings.Split(string(msg), "\n") {
			// A batched frame ends with a newline; the empty tail would
			// otherwise render as a blank line between blocks.
			if strings.TrimSpace(line) == "" {
				continue
			}
			// Also try per-line JSON (for history blob where each line is a WireMessage JSON)
			var wl WireMessage
			if hs, ok := parseHistorySync([]byte(line)); ok {
				s.handleHistorySync(hs)
				continue
			}
			if err := json.Unmarshal([]byte(line), &wl); err == nil && (wl.Type == "chat" || wl.Type == "system") {
				if wire.HasTag(wl.Tags, wire.TagHistory) {
					s.handleReplayMarker(wl)
					continue
				}
				s.Display.DisplayMu.Lock()
				if s.Chain.Loading && s.Chain.InSync && !s.isLoadHistoryWire(wl) {
					s.Chain.LoadHold = append(s.Chain.LoadHold, heldLine{msg: wl})
					s.Display.DisplayMu.Unlock()
					continue
				}
				wasOlder := s.Chain.InOlder
				liveOlder := wasOlder && !s.isOlderHistoryWire(wl)
				if liveOlder {
					s.flushDateBannerLocked()
					s.Chain.InOlder = false
				}
				rendered := s.verifyReplayWire(wl, wl.Type == "chat")
				if !rendered && wl.Type == "system" && !isShowingJoin && wire.HasTag(wl.Tags, wire.TagDate) && !liveOlder {
					s.Pending.PendingDateBannerWire = &wl
					s.Chain.InOlder = wasOlder
					s.Display.DisplayMu.Unlock()
					continue
				}
				if !rendered && wl.Type == "system" && !isShowingJoin && wire.HasAnyTag(wl.Tags, wire.TagJoin, wire.TagLeave) {
					s.Chain.InOlder = wasOlder
					s.Display.DisplayMu.Unlock()
					continue
				}
				if !rendered {
					if !liveOlder {
						s.flushDateBannerLocked()
					}
					s.renderChatBlock(wl)
				}
				s.Chain.InOlder = wasOlder
				s.Display.DisplayMu.Unlock()
				continue
			}
			if !isShowingJoin && s.Pending.PendingDateBannerWire != nil {
				s.Display.DisplayMu.Lock()
				s.flushDateBannerLocked()
				s.Display.DisplayMu.Unlock()
			}
			s.Display.DisplayMu.Lock()
			if s.Chain.Loading && s.Chain.InSync {
				s.Chain.LoadHold = append(s.Chain.LoadHold, heldLine{raw: line, isRaw: true})
				s.Display.DisplayMu.Unlock()
				continue
			}
			s.emitRawLineLocked(line)
			s.Display.DisplayMu.Unlock()
		}
		s.refreshCoalesced()
	}
}

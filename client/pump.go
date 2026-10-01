package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/CleveTok3125/V2V/internal/filter"
	"github.com/CleveTok3125/V2V/internal/trip"
	"github.com/CleveTok3125/V2V/internal/wire"
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
// before the block that follows it. Caller must hold s.Display.DisplayMu.
func (s *Session) flushDateBannerLocked() {
	if s.Pending.PendingDateBannerWire != nil {
		msg := s.Pending.PendingDateBannerWire
		s.Pending.PendingDateBannerWire = nil
		if s.showDateBannerLocked(msg.SysDate) {
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
	s.emitTabLive(true, TabChat, fmt.Sprintf("| %s\n", filter.SanitizeForDisplay(line)))
}

// trackReplayWindow updates the replay window flags for one boundary
// line. A join/load header raises InSync (full verification); the
// on-demand segment header raises InOlder (render and index only —
// older heights can never verify against the running tip); the
// recovery header raises InRecover (render, index, and verify against
// the refill anchor). Any footer flushes a stashed date banner first
// (otherwise a banner closing the window is silently dropped) and then
// clears all three. Caller must hold DisplayMu.
func (s *Session) trackReplayWindow(tags []string, start bool) {
	if start {
		switch {
		case wire.HasTag(tags, wire.TagHistoryOlder):
			s.Chain.InOlder = true
		case wire.HasTag(tags, wire.TagHistoryRecover):
			s.Chain.InRecover = true
		default:
			s.Chain.InSync = true
		}
		return
	}
	s.flushDateBannerLocked()
	s.Chain.InOlder = false
	s.Chain.InRecover = false
	s.Pending.PendingDateBannerWire = nil
	s.Chain.InSync = false
}

// handleReplayMarker applies one replay window marker. Markers are tagged,
// so which window a line opens or closes comes from the wire rather than
// from its wording; the text is only ever drawn.
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
	// A page during the client-driven load closes with its own
	// header/footer; printing them per page would pepper the stream with
	// markers. The load prints one banner up front and the merge hides
	// recovery markers, so all of them stay hidden while Loading.
	recovery := wire.HasTag(tags, wire.TagHistoryRecover)
	if !(s.Chain.Loading || recovery) {
		s.emitTab(TabChat, fmt.Sprintf("| %s\n", filter.SanitizeForDisplay(msg.Text)))
	}
	// A recovery footer settles its refill after the footer line, so the
	// confirmation reads as a result of the window instead of preceding its
	// own footer.
	if !start && recovery {
		s.finishRecovery()
	}
	s.Display.DisplayMu.Unlock()
}

// handleHistoryInfo starts the client-driven initial load: the server
// announces the available window, the client holds live output and
// pages ascending until it has initialLines or the server reports no
// more. Caller refreshes after.
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
	s.Chain.SyncHeights = map[uint64][32]byte{}
	s.Chain.LoadTarget = target
	s.Chain.LoadLoaded = 0
	s.Chain.LoadRetried = 0
	s.Chain.LoadMaxSeq = info.MaxSeq
	// Announce the sync as a local line so a slow/large load does not
	// look like a hang.
	s.emitLocalFeedbackTags(wire.WithTags(wire.TagHistory), "| [Local]: Đang tải lịch sử...\n")
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

// finishLoadLocked ends the initial load: release the hold and run the
// fork check against the received heights. Caller must hold DisplayMu.
func (s *Session) finishLoadLocked() {
	s.Chain.Loading = false
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
// initial load; a recovery trailer settles its refill; anything else
// (a dropped recovery footer) is settled if a refill is pending.
// Caller refreshes after.
func (s *Session) handleHistorySync(hs HistorySync) {
	s.Display.DisplayMu.Lock()
	s.Chain.InSync = false
	if s.Chain.InRecover {
		// The recovery footer never arrived; settle at the trailer.
		s.Chain.InRecover = false
		s.finishRecovery()
		s.Display.DisplayMu.Unlock()
		return
	}
	if s.Chain.Loading && hs.Direction == "after" {
		s.onLoadPageLocked(hs)
		s.Display.DisplayMu.Unlock()
		return
	}
	// Non-load trailer: a recovery whose footer was dropped is still
	// pending (a present footer settles it at the pump, after its line).
	if s.Chain.RecoverPending != nil {
		s.finishRecovery()
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
				// A held catch-up must print before the exit path so
				// its buffered history is not lost with the process.
				s.Display.DisplayMu.Lock()
				s.releaseCatchupLocked()
				s.flushPendingGreetingLocked()
				s.Display.DisplayMu.Unlock()
				// Lost-connection line rides the same queue so it never
				// interleaves with (or jumps ahead of) queued chat lines.
				s.enqueueOutput("\r\033[K\n ❌ Mất kết nối server\n")
				// Flush before exit: os.Exit skips deferred
				// s.Display.Term.Close/flushChainTip, losing the newest tip and
				// leaving the terminal raw.
				s.flushChainTip()
				s.flushOutputNow()
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
			if rendered := s.verifyReplayWire(chatWire, true); !rendered {
				s.flushDateBannerLocked()
				s.renderChatBlock(chatWire)
			}
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
			rendered := s.verifyReplayWire(sysWire, false)
			if !rendered && !isShowingJoin && wire.HasTag(sysWire.Tags, wire.TagDate) {
				s.Pending.PendingDateBannerWire = &sysWire
				s.Display.DisplayMu.Unlock()
				s.refreshCoalesced()
				continue
			}
			if !rendered && !isShowingJoin && wire.HasAnyTag(sysWire.Tags, wire.TagJoin, wire.TagLeave) {
				s.Display.DisplayMu.Unlock()
				s.refreshCoalesced()
				continue
			}
			if !rendered {
				s.flushDateBannerLocked()
				s.renderChatBlock(sysWire)
			}
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
			// otherwise render as a blank "|" line between blocks.
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
				rendered := s.verifyReplayWire(wl, wl.Type == "chat")
				if !rendered && wl.Type == "system" && !isShowingJoin && wire.HasTag(wl.Tags, wire.TagDate) {
					s.Pending.PendingDateBannerWire = &wl
					s.Display.DisplayMu.Unlock()
					continue
				}
				if !rendered && wl.Type == "system" && !isShowingJoin && wire.HasAnyTag(wl.Tags, wire.TagJoin, wire.TagLeave) {
					s.Display.DisplayMu.Unlock()
					continue
				}
				if !rendered {
					s.flushDateBannerLocked()
					s.renderChatBlock(wl)
				}
				s.Display.DisplayMu.Unlock()
				continue
			}
			if !isShowingJoin && s.Pending.PendingDateBannerWire != nil {
				s.Display.DisplayMu.Lock()
				s.flushDateBannerLocked()
				s.Display.DisplayMu.Unlock()
			}
			if isTripBadgeLine(line) {
				s.Verify.AutoVerifyMu.RLock()
				av := s.Verify.AutoVerify
				s.Verify.AutoVerifyMu.RUnlock()
				if av {
					if job, ok := parseTripBadgeLine(line); ok {
						// Drop-oldest on full: dropped is treated as verify fail (deterministic)
						// Show the line immediately as pending-plain then queue newest for real verify
						// If queue was full, oldest was dropped and will stay uncolored (fail)
						s.enqueueVerify(job)
						continue
					}
				}
			}
			s.Display.DisplayMu.Lock()
			s.emitRawLineLocked(line)
			s.Display.DisplayMu.Unlock()
		}
		s.refreshCoalesced()
	}
}

// runVerify resolves trip badges asynchronously; dropped-queue
// entries stay red. Started once from main.
func (s *Session) runVerify() {

	for job := range s.Verify.VerifyCh {
		// Use shared trip verification (same as server) — serverPub is enforced to server's own key
		serverPub := strings.ToLower(job.serverPub)
		if serverPub == "" {
			serverPub = strings.ToLower(s.Challenge.ServerPubKey)
		}
		textForVerify := job.textParam
		// Links without text= verify the signature over msgHash alone;
		// nothing is displayed from the link, so no text binding exists.
		_, err := trip.Verify(trip.VerifyParams{
			Text:          textForVerify,
			DisplayName:   job.displayName,
			ServerPub:     serverPub,
			PubHex:        job.pub,
			Seq:           job.seq,
			PrevHex:       job.prev,
			SigHex:        job.sig,
			MsgHashHex:    job.msgHash,
			TmpID:         job.tmpID,
			ReplyTo:       job.tmpReplyTo,
			SkipTextCheck: textForVerify == "",
		})
		valid := err == nil
		// Fallback: if textParam was empty but msgHash check failed, try empty text path
		if !valid && textForVerify != "" {
			// Already handled; keep invalid
		}
		var colored string
		if valid {
			colored = badgeColor(job.badge) + job.badge + "\x1b[0m"
		} else {
			colored = "\x1b[91m" + job.badge + " ✗\x1b[0m"
		}
		line := fmt.Sprintf("  └─ ✍️ \x1b]8;;%s\x1b\\%s\x1b]8;;\x1b\\", s.absoluteVerifyURL(job.urlStr), colored)
		s.Display.DisplayMu.Lock()
		s.emitTab(TabChat, fmt.Sprintf("| %s\n", filter.SanitizeForDisplay(line)))
		s.Display.DisplayMu.Unlock()
		s.refreshCoalesced()
	}
}

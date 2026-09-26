package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/CleveTok3125/V2V/internal/filter"
	"github.com/CleveTok3125/V2V/internal/trip"
)

// Session read pump and async verify worker (moved from main).

// flushDateBannerLocked prints a stashed date banner to TabSystem
// before the block that follows it. Caller must hold s.Display.DisplayMu.
func (s *Session) flushDateBannerLocked() {
	if s.Pending.PendingDateBanner != "" {
		s.emitTab(TabSystem, fmt.Sprintf("| %s\n", filter.SanitizeForDisplay(s.Pending.PendingDateBanner)))
		s.Pending.PendingDateBanner = ""
	}
	if s.Pending.PendingDateBannerWire != nil {
		s.renderChatBlock(*s.Pending.PendingDateBannerWire)
		s.Pending.PendingDateBannerWire = nil
	}
}

// trackReplayWindow updates the replay window flags for one boundary
// line. The join header raises InSync (full verification); the
// on-demand segment header raises InOlder (render and index only —
// older heights can never verify against the running tip); any footer
// flushes a stashed date banner first (otherwise a banner closing the
// window is silently dropped) and then clears both. Caller must hold
// DisplayMu.
func (s *Session) trackReplayWindow(line string, start bool) {
	if start {
		if isOlderSegmentHeader(line) {
			s.Chain.InOlder = true
		} else {
			s.Chain.InSync = true
		}
		return
	}
	s.flushDateBannerLocked()
	s.Chain.InOlder = false
	s.Pending.PendingDateBanner = ""
	s.Pending.PendingDateBannerWire = nil
	s.Chain.InSync = false
}

// handleHistorySync consumes a replay trailer from either a whole
// frame or a coalesced per-line blob: never rendered, only the
// fork check runs. Caller refreshes after.
func (s *Session) handleHistorySync(hs HistorySync) {
	s.Display.DisplayMu.Lock()
	s.Chain.InSync = false
	if warn, flush := forkWarning(hs, s.Chain.HavePersistedTip, s.Chain.PersistedTip, s.Chain.PersistedHeight, s.Chain.SyncHashes); warn != "" {
		s.emitLocalFeedback(warn)
		if flush {
			s.flushChainTip()
		}
	}
	s.Display.DisplayMu.Unlock()
}

// runPump reads server frames (chat/system/history/trailer) and
// renders them. Started once from main.
func (s *Session) runPump() {
	if s.PumpDone != nil {
		defer close(s.PumpDone)
	}

	for {
		_, msg, err := s.Conn.ReadMessage()
		if err != nil {
			select {
			case <-s.Quitting:
				return
			default:
				fmt.Fprintf(s.Display.Out, "\r\033[K\n ❌ Mất kết nối server\n")
				// Flush before exit: os.Exit skips deferred
				// s.Display.Term.Close/flushChainTip, losing the newest tip and
				// leaving the terminal raw.
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
		var wire WireMessage
		if err := json.Unmarshal(msg, &wire); err == nil && wire.Type == "chat" {
			s.Display.DisplayMu.Lock()
			s.verifyReplayWire(wire, true)
			s.flushDateBannerLocked()
			s.renderChatBlock(wire)
			s.Display.DisplayMu.Unlock()
			s.refreshCoalesced()
			continue
		}
		var sysWire WireMessage
		if err := json.Unmarshal(msg, &sysWire); err == nil && sysWire.Type == "system" {
			s.Display.DisplayMu.Lock()
			s.verifyReplayWire(sysWire, false)
			if !isShowingJoin && isDateBanner(sysWire) {
				s.Pending.PendingDateBannerWire = &sysWire
				s.Display.DisplayMu.Unlock()
				s.refreshCoalesced()
				continue
			}
			if !isShowingJoin && isJoinLeave(sysWire) {
				s.Display.DisplayMu.Unlock()
				s.refreshCoalesced()
				continue
			}
			s.flushDateBannerLocked()
			s.renderChatBlock(sysWire)
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
		// In-chat PoW offer: verify and solve in background so the
		// pump never blocks on proof-of-work.
		var offer PowOffer
		if err := json.Unmarshal(msg, &offer); err == nil && offer.Type == "pow_offer" {
			go s.handlePowOffer(offer)
			s.refreshCoalesced()
			continue
		}
		for _, line := range strings.Split(string(msg), "\n") {
			// Also try per-line JSON (for history blob where each line is a WireMessage JSON)
			var wl WireMessage
			if hs, ok := parseHistorySync([]byte(line)); ok {
				s.handleHistorySync(hs)
				continue
			}
			if err := json.Unmarshal([]byte(line), &wl); err == nil && (wl.Type == "chat" || wl.Type == "system") {
				s.Display.DisplayMu.Lock()
				s.verifyReplayWire(wl, wl.Type == "chat")
				if wl.Type == "system" && !isShowingJoin && isDateBanner(wl) {
					s.Pending.PendingDateBannerWire = &wl
					s.Display.DisplayMu.Unlock()
					continue
				}
				if wl.Type == "system" && !isShowingJoin && isJoinLeave(wl) {
					s.Display.DisplayMu.Unlock()
					continue
				}
				s.flushDateBannerLocked()
				s.renderChatBlock(wl)
				s.Display.DisplayMu.Unlock()
				continue
			}
			if !isShowingJoin && isDateBannerLine(line) {
				s.Pending.PendingDateBanner = line
				continue
			}
			if !isShowingJoin && isJoinLeaveSystemLine(line) {
				continue
			}
			if boundary, start := parseHistoryBoundary(line); boundary {
				s.Display.DisplayMu.Lock()
				s.trackReplayWindow(line, start)
				s.emitTab(TabChat, fmt.Sprintf("| %s\n", filter.SanitizeForDisplay(line)))
				s.Display.DisplayMu.Unlock()
				continue
			}
			if !isShowingJoin && (s.Pending.PendingDateBanner != "" || s.Pending.PendingDateBannerWire != nil) {
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
			s.emitTab(classifyTab(line), fmt.Sprintf("| %s\n", filter.SanitizeForDisplay(line)))
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
		line := fmt.Sprintf("  └─ ✍️ \x1b]8;;%s\x1b\\%s\x1b]8;;\x1b\\", job.urlStr, colored)
		s.Display.DisplayMu.Lock()
		s.emitTab(TabChat, fmt.Sprintf("| %s\n", filter.SanitizeForDisplay(line)))
		s.Display.DisplayMu.Unlock()
		s.refreshCoalesced()
	}
}

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
// before the block that follows it. Caller must hold s.DisplayMu.
func (s *Session) flushDateBannerLocked() {
	if s.PendingDateBanner != "" {
		s.emitTab(TabSystem, fmt.Sprintf("| %s\n", filter.SanitizeForDisplay(s.PendingDateBanner)))
		s.PendingDateBanner = ""
	}
	if s.PendingDateBannerWire != nil {
		s.renderChatBlock(*s.PendingDateBannerWire)
		s.PendingDateBannerWire = nil
	}
}

// handleHistorySync consumes a replay trailer from either a whole
// frame or a coalesced per-line blob: never rendered, only the
// fork check runs. Caller refreshes after.
func (s *Session) handleHistorySync(hs HistorySync) {
	s.DisplayMu.Lock()
	s.InSync = false
	if warn, flush := forkWarning(hs, s.HavePersistedTip, s.PersistedTip, s.PersistedHeight, s.SyncHashes); warn != "" {
		s.emitLocalFeedback(warn)
		if flush {
			s.flushChainTip()
		}
	}
	s.DisplayMu.Unlock()
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
				fmt.Fprintf(s.Out, "\r\033[K\n ❌ Mất kết nối server\n")
				// Flush before exit: os.Exit skips deferred
				// s.Term.Close/flushChainTip, losing the newest tip and
				// leaving the terminal raw.
				s.flushChainTip()
				s.Term.Close()
				ClearLoadedPassphrase()
				os.Exit(1)
			}
		}

		s.ShowJoinMu.RLock()
		isShowingJoin := s.ShowJoinLeave
		s.ShowJoinMu.RUnlock()

		// Try to handle structured WireMessage JSON first (for new protocol)
		var wire WireMessage
		if err := json.Unmarshal(msg, &wire); err == nil && wire.Type == "chat" {
			s.DisplayMu.Lock()
			s.consumeEchoLocked(wire, true)
			s.checkChainLink(wire)
			s.flushDateBannerLocked()
			s.renderChatBlock(wire)
			s.DisplayMu.Unlock()
			s.refreshCoalesced()
			continue
		}
		var sysWire WireMessage
		if err := json.Unmarshal(msg, &sysWire); err == nil && sysWire.Type == "system" {
			s.DisplayMu.Lock()
			s.checkChainLink(sysWire)
			if !isShowingJoin && isDateBanner(sysWire) {
				s.PendingDateBannerWire = &sysWire
				s.DisplayMu.Unlock()
				s.refreshCoalesced()
				continue
			}
			if !isShowingJoin && isJoinLeave(sysWire) {
				s.DisplayMu.Unlock()
				s.refreshCoalesced()
				continue
			}
			s.flushDateBannerLocked()
			s.renderChatBlock(sysWire)
			s.DisplayMu.Unlock()
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
		for _, line := range strings.Split(string(msg), "\n") {
			// Also try per-line JSON (for history blob where each line is a WireMessage JSON)
			var wl WireMessage
			if hs, ok := parseHistorySync([]byte(line)); ok {
				s.handleHistorySync(hs)
				continue
			}
			if err := json.Unmarshal([]byte(line), &wl); err == nil && (wl.Type == "chat" || wl.Type == "system") {
				s.DisplayMu.Lock()
				if wl.Type == "chat" {
					s.consumeEchoLocked(wl, !s.InSync)
				}
				s.checkChainLink(wl)
				if wl.Type == "system" && !isShowingJoin && isDateBanner(wl) {
					s.PendingDateBannerWire = &wl
					s.DisplayMu.Unlock()
					continue
				}
				if wl.Type == "system" && !isShowingJoin && isJoinLeave(wl) {
					s.DisplayMu.Unlock()
					continue
				}
				s.flushDateBannerLocked()
				s.renderChatBlock(wl)
				s.DisplayMu.Unlock()
				continue
			}
			if !isShowingJoin && isDateBannerLine(line) {
				s.PendingDateBanner = line
				continue
			}
			if !isShowingJoin && isJoinLeaveSystemLine(line) {
				continue
			}
			if boundary, start := parseHistoryBoundary(line); boundary {
				s.DisplayMu.Lock()
				if start {
					s.InSync = true
				} else {
					s.PendingDateBanner = ""
					s.PendingDateBannerWire = nil
					s.InSync = false
				}
				s.emitTab(TabChat, fmt.Sprintf("| %s\n", filter.SanitizeForDisplay(line)))
				s.DisplayMu.Unlock()
				continue
			}
			if !isShowingJoin && (s.PendingDateBanner != "" || s.PendingDateBannerWire != nil) {
				s.DisplayMu.Lock()
				s.flushDateBannerLocked()
				s.DisplayMu.Unlock()
			}
			if isTripBadgeLine(line) {
				s.AutoVerifyMu.RLock()
				av := s.AutoVerify
				s.AutoVerifyMu.RUnlock()
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
			s.DisplayMu.Lock()
			s.emitTab(classifyTab(line), fmt.Sprintf("| %s\n", filter.SanitizeForDisplay(line)))
			s.DisplayMu.Unlock()
		}
		s.refreshCoalesced()
	}
}

// runVerify resolves trip badges asynchronously; dropped-queue
// entries stay red. Started once from main.
func (s *Session) runVerify() {

	for job := range s.VerifyCh {
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
		s.DisplayMu.Lock()
		s.emitTab(TabChat, fmt.Sprintf("| %s\n", filter.SanitizeForDisplay(line)))
		s.DisplayMu.Unlock()
		s.refreshCoalesced()
	}
}

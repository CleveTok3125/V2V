package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/CleveTok3125/V2V/internal/filter"
	"github.com/CleveTok3125/V2V/internal/trip"
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
	if s.Pending.PendingDateBanner != "" {
		text := s.Pending.PendingDateBanner
		s.Pending.PendingDateBanner = ""
		s.emitDateBannerLocked(text)
	}
	if s.Pending.PendingDateBannerWire != nil {
		wire := s.Pending.PendingDateBannerWire
		s.Pending.PendingDateBannerWire = nil
		if wire.Text != s.Display.LastDateBanner {
			s.Display.LastDateBanner = wire.Text
			s.renderChatBlock(*wire)
		}
	}
}

// emitDateBannerLocked prints one date banner unless it repeats the
// previous one: a server may announce the current date on connect right
// after the replay already carried it, and two identical banners read
// as noise. Caller must hold DisplayMu.
func (s *Session) emitDateBannerLocked(text string) {
	if text == "" || text == s.Display.LastDateBanner {
		return
	}
	s.Display.LastDateBanner = text
	s.emitTab(TabSystem, fmt.Sprintf("| %s\n", filter.SanitizeForDisplay(text)))
}

// trackReplayWindow updates the replay window flags for one boundary
// line. The join header raises InSync (full verification); the
// on-demand segment header raises InOlder (render and index only —
// older heights can never verify against the running tip); the
// recovery header raises InRecover (render, index, and verify against
// the refill anchor in RecoverPending). Any footer flushes a stashed
// date banner first (otherwise a banner closing the window is
// silently dropped) and then clears all three. Caller must hold
// DisplayMu.
func (s *Session) trackReplayWindow(line string, start bool) {
	if start {
		// A new window supersedes any join whose trailer never came.
		s.Chain.SyncClosed = false
		if isOlderSegmentHeader(line) {
			s.Chain.InOlder = true
		} else if isRecoveryHeader(line) {
			s.Chain.InRecover = true
		} else {
			s.Chain.InSync = true
			// A fresh join window: heights from any earlier replay are
			// stale and must not mask drops in this one.
			s.Chain.SyncHeights = map[uint64][32]byte{}
			// Hold the replay until its trailer says whether lines were
			// dropped: merging refills into an already-printed stream
			// is impossible, and a gapped print would linger in
			// scrollback. The timer releases a stuck hold.
			s.Display.CatchupHold = true
			s.Display.HoldGen++
			gen := s.Display.HoldGen
			s.Display.HoldTimer = time.AfterFunc(catchupHoldMax, func() {
				s.Display.DisplayMu.Lock()
				if s.Display.HoldGen == gen {
					s.releaseCatchupLocked()
				}
				s.Display.DisplayMu.Unlock()
			})
		}
		return
	}
	wasJoin := s.Chain.InSync
	s.flushDateBannerLocked()
	s.Chain.InOlder = false
	s.Chain.InRecover = false
	s.Pending.PendingDateBanner = ""
	s.Pending.PendingDateBannerWire = nil
	s.Chain.InSync = false
	// A join footer precedes its trailer: remember it so the trailer
	// can refill the heights the replay dropped.
	if wasJoin {
		s.Chain.SyncClosed = true
	}
}

// handleHistorySync consumes a replay trailer from either a whole
// frame or a coalesced per-line blob. Only the join replay records its
// received heights (SyncHeights), so only its trailer can judge a fork;
// segment and recovery trailers carry no such set. A recovery trailer
// settles its refill instead. Caller refreshes after.
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
	if s.Chain.SyncClosed {
		s.Chain.SyncClosed = false
		// The join replay reports how many lines it lost; recover those
		// heights before the fork check (which also skips on drops).
		s.recoverMissedFromTrailer(hs)
		if s.Chain.RecoverPending == nil {
			// Nothing to refill: print the replay now.
			s.releaseCatchupLocked()
		}
		if warn, flush := forkWarning(hs, s.Chain.HavePersistedTip, s.Chain.PersistedTip, s.Chain.PersistedHeight, s.Chain.SyncHeights); warn != "" {
			s.emitLocalFeedback(warn)
			if flush {
				s.flushChainTip()
			}
		}
		s.Chain.SyncHeights = map[uint64][32]byte{}
		s.Display.DisplayMu.Unlock()
		return
	}
	// Non-join trailer: a recovery whose footer was dropped is still
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
		var wire WireMessage
		if err := json.Unmarshal(msg, &wire); err == nil && wire.Type == "chat" {
			s.Display.DisplayMu.Lock()
			if rendered := s.verifyReplayWire(wire, true); !rendered {
				s.flushDateBannerLocked()
				s.renderChatBlock(wire)
			}
			s.Display.DisplayMu.Unlock()
			s.refreshCoalesced()
			continue
		}
		var sysWire WireMessage
		if err := json.Unmarshal(msg, &sysWire); err == nil && sysWire.Type == "system" {
			s.Display.DisplayMu.Lock()
			rendered := s.verifyReplayWire(sysWire, false)
			if !rendered && !isShowingJoin && isDateBanner(sysWire) {
				s.Pending.PendingDateBannerWire = &sysWire
				s.Display.DisplayMu.Unlock()
				s.refreshCoalesced()
				continue
			}
			if !rendered && !isShowingJoin && isJoinLeave(sysWire) {
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
				rendered := s.verifyReplayWire(wl, wl.Type == "chat")
				if !rendered && wl.Type == "system" && !isShowingJoin && isDateBanner(wl) {
					s.Pending.PendingDateBannerWire = &wl
					s.Display.DisplayMu.Unlock()
					continue
				}
				if !rendered && wl.Type == "system" && !isShowingJoin && isJoinLeave(wl) {
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
				// Recovery boundaries are internal plumbing: the merge
				// places the refilled lines inline and one notice
				// reports the outcome, so the markers stay hidden.
				if !isRecoveryHeader(line) && !isRecoveryFooter(line) {
					s.emitTab(TabChat, fmt.Sprintf("| %s\n", filter.SanitizeForDisplay(line)))
				}
				// A recovery footer settles its refill after the footer
				// line, so the confirmation reads as a result of the
				// window instead of preceding its own footer.
				if !start && isRecoveryFooter(line) {
					s.finishRecovery()
				}
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

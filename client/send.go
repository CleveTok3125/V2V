package main

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/CleveTok3125/V2V/internal/filter"
	"github.com/CleveTok3125/V2V/internal/guard"
	"github.com/CleveTok3125/V2V/internal/markup"
	"github.com/CleveTok3125/V2V/internal/tripcolor"
)

// Session send path (moved from main).

const maxPendingPlaceholders = 128

// collectBody gathers a multi-line codeblock when fenced.
func (s *Session) collectBody(text string) (string, int, bool) {
	typedLinesCount := 1

	if strings.HasPrefix(text, "```") {
		if !markup.NeedsContinuation(text) {
			// Single-line fence (```code```): complete already.
			typedLinesCount = 1
		} else {
			var canceled bool
			text, canceled = collectCodeblock(s.Display.Term, text)
			if canceled {
				return text, 0, false
			}
			typedLinesCount = strings.Count(text, "\n") + 1
		}
	}
	return text, typedLinesCount, true
}

// checkSendGuards runs the client-side content and cooldown gates.
func (s *Session) checkSendGuards(text string) bool {
	if err := filter.ValidateMessage(text); err != nil {
		s.Display.DisplayMu.Lock()
		s.emitLocalFeedback(fmt.Sprintf("| [Local]: Tin nhắn chứa ký tự không hợp lệ và đã bị chặn (client-side): %v\n", err))
		s.Display.DisplayMu.Unlock()
		s.Display.Term.Refresh()
		return false
	}

	// Guard: client-side MessageCooldown (mirror server, zero-trust)
	if ClientCfg != nil {
		if err := guard.ValidateMessageForSend(text, s.Pending.LastMessageTime, &guard.Limits{
			MaxMessageLength: ClientCfg.Limits.MaxMessageLength,
			MaxMessageLine:   ClientCfg.Limits.MaxMessageLine,
			MessageCooldown:  ClientCfg.Limits.MessageCooldown,
		}, false); err != nil {
			if err == guard.ErrTooFast {
				s.Display.DisplayMu.Lock()
				s.emitLocalFeedback(fmt.Sprintf("| [Local]: Bạn đang chat quá nhanh! Vui lòng đợi %v.\n", ClientCfg.Limits.MessageCooldown))
				s.Display.DisplayMu.Unlock()
				s.Display.Term.Refresh()
				return false
			}
			if err == guard.ErrTooLong {
				s.Display.DisplayMu.Lock()
				s.emitLocalFeedback(fmt.Sprintf("| [Local]: Tin nhắn quá dài (tối đa %d ký tự).\n", ClientCfg.Limits.MaxMessageLength))
				s.Display.DisplayMu.Unlock()
				s.Display.Term.Refresh()
				return false
			}
		}
	}
	return true
}

// renderPlaceholder wipes the input line and prints the grey pending block.
func (s *Session) renderPlaceholder(text string, typedLinesCount int) (phRows int, phShown bool, phBufEnd int) {
	// Placeholder: keep original text grey with pending indicator until server echo
	// Single s.Display.DisplayMu lock for entire wipe + placeholder + trip sign + send to avoid burst drift
	phRows = 0
	phShown = s.Display.ActiveTab == TabChat
	phBufEnd = 0
	s.Display.DisplayMu.Lock()
	for range typedLinesCount {
		fmt.Fprint(s.Display.Out, "\033[1A\033[2K\r")
	}

	// Render markup on the whole text first so fenced blocks
	// keep their state across lines; phRows then counts rendered
	// rows (headers added, closers dropped), matching the erase math.
	// Plain rendering (no highlight): highlight's full resets would
	// cancel the grey placeholder wrapper mid-line, and line counts
	// match the highlighted echo anyway.
	lines := strings.Split(markup.SpanPlain(text), "\n")
	phRows = len(lines)
	// Quoted target previews first (same helper as the echo path, so
	// both blocks share the shape; pending quotes carry ⏳ so the
	// erase region check keeps passing).
	if s.Pending.PendingReplyTo > 0 {
		for _, q := range s.quoteLinesFor(s.Pending.PendingReplyTo, true) {
			s.emitTab(TabChat, q+"\n")
			phRows++
		}
	}
	for i, line := range lines {
		line = markup.Linkify(line)
		if i == 0 {
			s.emitTab(TabChat, fmt.Sprintf("\x1b[90m| Bạn: %s ⏳\x1b[0m\n", line))
		} else {
			s.emitTab(TabChat, fmt.Sprintf("\x1b[90m|      %s\x1b[0m\n", line))
		}
	}
	// Trip placeholder (grey ◆ …) — real badge will come from server echo
	if s.TripPriv != nil || CLI.Tripcode != "" {
		badgePlaceholder := s.TripBadge
		if badgePlaceholder == "" && CLI.Tripcode != "" {
			h := sha256.Sum256([]byte(CLI.Tripcode))
			badgePlaceholder = hex.EncodeToString(h[:])[:8]
		}
		if badgePlaceholder != "" {
			s.emitTab(TabChat, fmt.Sprintf("\x1b[90m|  └─ ✍ ◆ %s ⏳\x1b[0m\n", badgePlaceholder))
			phRows++
		}
	}
	// Trailing meta line: every block ends with exactly one meta row so
	// the echo (carrying the real #height:hash) replaces it in place.
	// The chain position is unknown until the server echo arrives.
	// Hidden with /meta off; the echo follows the same session flag,
	// so row counts stay consistent.
	s.Display.ShowMetaMu.RLock()
	pmMeta := s.Display.ShowMeta
	s.Display.ShowMetaMu.RUnlock()
	if pmMeta {
		s.emitTab(TabChat, "\x1b[90m|   └─  ··· ⏳\x1b[0m\n")
		phRows++
	}
	phBufEnd = len(s.Display.TabChat.lines)
	s.Display.Term.Refresh()
	s.Display.DisplayMu.Unlock()
	return phRows, phShown, phBufEnd
}

// sendMessage signs (trip) or envelopes (plain) one message, tracks its
func (s *Session) sendMessage(text string, phRows int, phShown bool, phBufEnd int) error {
	s.Pending.TmpSeq++
	var err error
	if s.TripPriv != nil {
		// Sign message with trip chain — bind displayName for anti-spoof
		s.TripSeq++
		msgHash := sha256.Sum256([]byte(text))
		prevCopy := make([]byte, len(s.TripPrev))
		copy(prevCopy, s.TripPrev)
		payload := tripcolor.CanonicalPayload(strings.ToLower(s.Challenge.ServerPubKey), s.TripSeq, prevCopy, msgHash[:], []byte(s.TripPub), s.Username, s.Pending.TmpSeq, s.Pending.PendingReplyTo)
		sig := ed25519.Sign(s.TripPriv, payload)
		h := sha256.New()
		h.Write(prevCopy)
		h.Write(sig)
		h.Write(msgHash[:])
		newPrev := h.Sum(nil)
		copy(s.TripPrev, newPrev)
		tripMsg := TripMessage{Text: text, Pub: hex.EncodeToString([]byte(s.TripPub)), Seq: s.TripSeq, Prev: hex.EncodeToString(prevCopy), Sig: hex.EncodeToString(sig), DisplayName: s.Username, TmpID: s.Pending.TmpSeq, ReplyTo: s.Pending.PendingReplyTo}
		err = s.Conn.WriteJSON(tripMsg)
		if err != nil {
			// Rollback seq/prev on send failure to avoid permanent fork
			s.TripSeq--
			copy(s.TripPrev, prevCopy)
			s.Pending.TmpSeq--
		}
	} else {
		// Unsigned chat always travels in an envelope carrying the
		// session counter; raw text is rejected by the server.
		err = s.Conn.WriteJSON(PlainMessage{TmpID: s.Pending.TmpSeq, Text: text, ReplyTo: s.Pending.PendingReplyTo})
		if err != nil {
			s.Pending.TmpSeq--
		}
	}
	// Reply targets are one-shot: consumed by the send above whether
	// it succeeded or not (a failed send ends the session anyway).
	// Reset happens after placeholder tracking below, which records
	// the target for echo matching.
	if err != nil {
		// Mark placeholder as failed (red) is handled by server unicast; keep placeholder grey until then
		s.Pending.LastMessageTime = time.Now()
	} else {
		s.Pending.LastMessageTime = time.Now()
		// Track placeholder so the server echo can replace it.
		s.Display.DisplayMu.Lock()
		// Queue guard nested per Display -> Chain -> Pending; the
		// erasePlaceholderLocked call in this function stays on DisplayMu.
		s.Pending.Mu.Lock()
		pm := pendingMsg{text: text, rows: phRows, shown: phShown, gen: s.Display.PrintGen, bufEnd: phBufEnd, sentAt: time.Now(), tmpID: s.Pending.TmpSeq, replyTo: s.Pending.PendingReplyTo}
		if s.TripPriv != nil {
			pm.hasTrip = true
			pm.seq = s.TripSeq
			pm.pub = hex.EncodeToString([]byte(s.TripPub))
		}
		s.Pending.PendingPlaceholders = append(s.Pending.PendingPlaceholders, pm)
		// Bound the queue: echoes that never arrive (dead server, old
		// build) must not grow memory or turn matching quadratic.
		// Evicted entries stay grey on screen: honestly unconfirmed.
		// Linear scan stays trivial at this bound, so no index map.
		for len(s.Pending.PendingPlaceholders) > maxPendingPlaceholders {
			s.Pending.PendingPlaceholders = s.Pending.PendingPlaceholders[1:]
		}
		// The echo may have beaten us here (local echo race): if a
		// stashed echo matches, erase the placeholder at once. The
		// echo itself was already rendered when it arrived.
		var haveStashed bool
		s.Pending.PendingEchoes, _, haveStashed = takeStashedEcho(s.Pending.PendingEchoes, pm.tmpID)
		if haveStashed {
			for i, p := range s.Pending.PendingPlaceholders {
				if p.tmpID == pm.tmpID {
					s.Pending.PendingPlaceholders = append(s.Pending.PendingPlaceholders[:i], s.Pending.PendingPlaceholders[i+1:]...)
					break
				}
			}
			s.erasePlaceholderLocked(pm)
		}
		s.Pending.Mu.Unlock()
		s.Display.DisplayMu.Unlock()
	}
	// Reply targets are one-shot, cleared after tracking above (the
	// pending entry already captured the target for echo matching).
	s.Pending.PendingReplyTo = 0
	if err != nil {
		fmt.Println("❌ Lỗi gửi tin nhắn:", err)
		return err
	}
	return nil
}

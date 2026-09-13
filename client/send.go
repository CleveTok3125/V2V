package main

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/CleveTok3125/V2V/internal/codebg"
	"github.com/CleveTok3125/V2V/internal/filter"
	"github.com/CleveTok3125/V2V/internal/guard"
	"github.com/CleveTok3125/V2V/internal/linkify"
	"github.com/CleveTok3125/V2V/internal/markup"
	"github.com/CleveTok3125/V2V/internal/tripcolor"
)

// Session send path (moved from main).

const maxPendingPlaceholders = 128

// collectBody gathers a multi-line codeblock when fenced.
func (s *Session) collectBody(text string) (string, int, bool) {
	typedLinesCount := 1

	if strings.HasPrefix(text, "```") {
		if !codebg.NeedsContinuation(text) {
			// Single-line fence (```code```): complete already.
			typedLinesCount = 1
		} else {
			var canceled bool
			text, canceled = collectCodeblock(s.Term, text)
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
		s.DisplayMu.Lock()
		s.emitLocalFeedback(fmt.Sprintf("| [Local]: Tin nhắn chứa ký tự không hợp lệ và đã bị chặn (client-side): %v\n", err))
		s.DisplayMu.Unlock()
		s.Term.Refresh()
		return false
	}

	// Guard: client-side MessageCooldown (mirror server, zero-trust)
	if ClientCfg != nil {
		if err := guard.ValidateMessageForSend(text, s.LastMessageTime, &guard.Limits{
			MaxMessageLength: ClientCfg.Limits.MaxMessageLength,
			MaxMessageLine:   ClientCfg.Limits.MaxMessageLine,
			MessageCooldown:  ClientCfg.Limits.MessageCooldown,
		}, false); err != nil {
			if err == guard.ErrTooFast {
				s.DisplayMu.Lock()
				s.emitLocalFeedback(fmt.Sprintf("| [Local]: Bạn đang chat quá nhanh! Vui lòng đợi %v.\n", ClientCfg.Limits.MessageCooldown))
				s.DisplayMu.Unlock()
				s.Term.Refresh()
				return false
			}
			if err == guard.ErrTooLong {
				s.DisplayMu.Lock()
				s.emitLocalFeedback(fmt.Sprintf("| [Local]: Tin nhắn quá dài (tối đa %d ký tự).\n", ClientCfg.Limits.MaxMessageLength))
				s.DisplayMu.Unlock()
				s.Term.Refresh()
				return false
			}
		}
	}
	return true
}

// renderPlaceholder wipes the input line and prints the grey pending block.
func (s *Session) renderPlaceholder(text string, typedLinesCount int) (phRows int, phShown bool, phBufEnd int) {
	// Placeholder: keep original text grey with pending indicator until server echo
	// Single s.DisplayMu lock for entire wipe + placeholder + trip sign + send to avoid burst drift
	phRows = 0
	phShown = s.ActiveTab == TabChat
	phBufEnd = 0
	s.DisplayMu.Lock()
	for range typedLinesCount {
		fmt.Fprint(s.Out, "\033[1A\033[2K\r")
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
	if s.PendingReplyTo > 0 {
		for _, q := range s.quoteLinesFor(s.PendingReplyTo, true) {
			s.emitTab(TabChat, q+"\n")
			phRows++
		}
	}
	for i, line := range lines {
		line = linkify.Linkify(line)
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
	s.ShowMetaMu.RLock()
	pmMeta := s.ShowMeta
	s.ShowMetaMu.RUnlock()
	if pmMeta {
		s.emitTab(TabChat, "\x1b[90m|   └─  ··· ⏳\x1b[0m\n")
		phRows++
	}
	phBufEnd = len(s.TabChat.lines)
	s.Term.Refresh()
	s.DisplayMu.Unlock()
	return phRows, phShown, phBufEnd
}

// sendMessage signs (trip) or envelopes (plain) one message, tracks its
func (s *Session) sendMessage(text string, phRows int, phShown bool, phBufEnd int) error {
	s.TmpSeq++
	var err error
	if s.TripPriv != nil {
		// Sign message with trip chain — bind displayName for anti-spoof
		s.TripSeq++
		msgHash := sha256.Sum256([]byte(text))
		prevCopy := make([]byte, len(s.TripPrev))
		copy(prevCopy, s.TripPrev)
		payload := tripcolor.CanonicalPayload(strings.ToLower(s.Challenge.ServerPubKey), s.TripSeq, prevCopy, msgHash[:], []byte(s.TripPub), s.Username, s.TmpSeq, s.PendingReplyTo)
		sig := ed25519.Sign(s.TripPriv, payload)
		h := sha256.New()
		h.Write(prevCopy)
		h.Write(sig)
		h.Write(msgHash[:])
		newPrev := h.Sum(nil)
		copy(s.TripPrev, newPrev)
		tripMsg := TripMessage{Text: text, Pub: hex.EncodeToString([]byte(s.TripPub)), Seq: s.TripSeq, Prev: hex.EncodeToString(prevCopy), Sig: hex.EncodeToString(sig), DisplayName: s.Username, TmpID: s.TmpSeq, ReplyTo: s.PendingReplyTo}
		var err error
		err = s.Conn.WriteJSON(tripMsg)
		if err != nil {
			// Rollback seq/prev on send failure to avoid permanent fork
			s.TripSeq--
			copy(s.TripPrev, prevCopy)
			s.TmpSeq--
		}
	} else {
		// Unsigned chat always travels in an envelope carrying the
		// session counter; raw text is rejected by the server.
		err = s.Conn.WriteJSON(PlainMessage{TmpID: s.TmpSeq, Text: text, ReplyTo: s.PendingReplyTo})
		if err != nil {
			s.TmpSeq--
		}
	}
	// Reply targets are one-shot: consumed by the send above whether
	// it succeeded or not (a failed send ends the session anyway).
	// Reset happens after placeholder tracking below, which records
	// the target for echo matching.
	if err != nil {
		// Mark placeholder as failed (red) is handled by server unicast; keep placeholder grey until then
		s.LastMessageTime = time.Now()
	} else {
		s.LastMessageTime = time.Now()
		// Track placeholder so the server echo can replace it.
		s.DisplayMu.Lock()
		pm := pendingMsg{text: text, rows: phRows, shown: phShown, gen: s.PrintGen, bufEnd: phBufEnd, sentAt: time.Now(), tmpID: s.TmpSeq, replyTo: s.PendingReplyTo}
		if s.TripPriv != nil {
			pm.hasTrip = true
			pm.seq = s.TripSeq
			pm.pub = hex.EncodeToString([]byte(s.TripPub))
		}
		s.PendingPlaceholders = append(s.PendingPlaceholders, pm)
		// Bound the queue: echoes that never arrive (dead server, old
		// build) must not grow memory or turn matching quadratic.
		// Evicted entries stay grey on screen: honestly unconfirmed.
		// Linear scan stays trivial at this bound, so no index map.
		for len(s.PendingPlaceholders) > maxPendingPlaceholders {
			s.PendingPlaceholders = s.PendingPlaceholders[1:]
		}
		// The echo may have beaten us here (local echo race): if a
		// stashed echo matches, erase the placeholder at once. The
		// echo itself was already rendered when it arrived.
		var haveStashed bool
		s.PendingEchoes, _, haveStashed = takeStashedEcho(s.PendingEchoes, pm.tmpID)
		if haveStashed {
			for i, p := range s.PendingPlaceholders {
				if p.tmpID == pm.tmpID {
					s.PendingPlaceholders = append(s.PendingPlaceholders[:i], s.PendingPlaceholders[i+1:]...)
					break
				}
			}
			s.erasePlaceholderLocked(pm)
		}
		s.DisplayMu.Unlock()
	}
	// Reply targets are one-shot, cleared after tracking above (the
	// pending entry already captured the target for echo matching).
	s.PendingReplyTo = 0
	if err != nil {
		fmt.Println("❌ Lỗi gửi tin nhắn:", err)
		return err
	}
	return nil
}

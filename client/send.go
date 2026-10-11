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

// inputEchoPrefix marks the row the person submitted, which the session now
// draws itself: readline erases the draft instead of committing it, so the
// line becomes a buffer entry like any other output and every later write for
// this message replaces that entry in place.
const (
	inputEchoPrefix         = "| > "
	inputContinuationPrefix = "| ... "
)

// inputEchoEntry renders submitted lines as one entry. A fenced block is one
// entry too, however many lines it grows to, because the message it becomes is
// one entry. Continuation rows reuse the prompt readline showed for them, so
// the echo reads as the same block the person typed.
func inputEchoEntry(lines []string) entry {
	rows := make([]string, 0, len(lines))
	for i, line := range lines {
		if i == 0 {
			rows = append(rows, inputEchoPrefix+line)
			continue
		}
		rows = append(rows, inputContinuationPrefix+line)
	}
	return entry{header: strings.Join(rows, "\n")}
}

// echoInputLine records the submitted line as a buffer entry and prints it.
func (s *Session) echoInputLine(text string) slotRef {
	s.Display.DisplayMu.Lock()
	defer s.Display.DisplayMu.Unlock()
	return s.appendEntrySlot(true, TabChat, inputEchoEntry([]string{text}))
}

// extendInputEcho rewrites the input echo as more lines arrive, so a fenced
// block reads as one growing entry rather than a row that stops changing.
func (s *Session) extendInputEcho(slot slotRef, lines []string) slotRef {
	next := inputEchoEntry(lines)
	s.Display.DisplayMu.Lock()
	defer s.Display.DisplayMu.Unlock()
	if !s.replaceSlot(slot, next, true) {
		return slot
	}
	return slotRef{index: slot.index, entry: next}
}

// collectBody gathers a multi-line codeblock when fenced, extending the input
// echo's entry as it goes.
func (s *Session) collectBody(text string, slot slotRef) (string, slotRef, bool) {
	if !strings.HasPrefix(text, "```") || !markup.NeedsContinuation(text) {
		// Single-line fence (```code```) or plain text: complete already.
		return text, slot, true
	}
	text, canceled := collectCodeblock(s.Display.Term, text, func(lines []string) {
		slot = s.extendInputEcho(slot, lines)
	})
	if canceled {
		return text, slot, false
	}
	return text, slot, true
}

// checkSendGuards runs the client-side content and cooldown gates.
func (s *Session) checkSendGuards(text string) bool {
	if err := filter.ValidateMessage(text); err != nil {
		s.Display.DisplayMu.Lock()
		s.emitLocalFeedback(fmt.Sprintf("| [Local]: Tin nhắn chứa ký tự không hợp lệ và đã bị chặn (client-side): %v\n", err))
		s.Display.DisplayMu.Unlock()
		s.refreshTerminal()
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
				s.refreshTerminal()
				return false
			}
			if err == guard.ErrTooLong {
				s.Display.DisplayMu.Lock()
				s.emitLocalFeedback(fmt.Sprintf("| [Local]: Tin nhắn quá dài (tối đa %d ký tự).\n", ClientCfg.Limits.MaxMessageLength))
				s.Display.DisplayMu.Unlock()
				s.refreshTerminal()
				return false
			}
		}
	}
	return true
}

// renderPlaceholder wipes the input line and prints the grey pending block.
// It returns both the number of buffer entries it wrote (sequence: quote
// rows, then the author header, then one row per rendered line) and the
// number of terminal rows those entries occupy. The buffer splice
// counts entries; the screen erase counts rows, and a line long enough to
// wrap makes the two differ.
func (s *Session) renderPlaceholder(text string, slot slotRef) (e entry, out slotRef, phShown bool) {
	// Placeholder: keep original text grey with pending indicator until the
	// server echo replaces this very entry.
	phShown = s.Display.ActiveTab == TabChat
	s.Display.DisplayMu.Lock()
	defer s.Display.DisplayMu.Unlock()

	// Render markup on the whole text first so fenced blocks
	// keep their state across lines; phRows then counts rendered
	// rows (headers added, closers dropped), matching the erase math.
	// Plain rendering (no highlight): highlight's full resets would
	// cancel the grey placeholder wrapper mid-line, and line counts
	// match the highlighted echo anyway.
	lines := strings.Split(markup.SpanPlain(text), "\n")
	// The whole placeholder is one entry, like the echo it stands in for: a
	// message is one entry whatever it carries. Splitting it into a row per
	// part would make the splice range and the erase math disagree with the
	// block that replaces it.
	// Quoted target previews come first, inside the same entry (same helper
	// as the echo path, so both shapes match).
	if s.Pending.PendingReplyTo > 0 {
		var quote strings.Builder
		for _, q := range s.quoteLinesFor(s.Pending.PendingReplyTo, true) {
			if quote.Len() > 0 {
				quote.WriteString("\n")
			}
			quote.WriteString(q)
		}
		e.quote = quote.String()
	}
	// The header carries everything the real one will: the author, the
	// chain placeholder, then the trip placeholder. The chain position and
	// the real badge only exist once the server echoes, so both stand in as
	// placeholders here and the echo replaces the whole block in place.
	s.Display.ShowMetaMu.RLock()
	pmMeta := s.Display.ShowMeta
	s.Display.ShowMetaMu.RUnlock()
	badgePlaceholder := ""
	if s.TripPriv != nil || CLI.Tripcode != "" {
		badgePlaceholder = s.TripBadge
		if badgePlaceholder == "" && CLI.Tripcode != "" {
			h := sha256.Sum256([]byte(CLI.Tripcode))
			badgePlaceholder = hex.EncodeToString(h[:])[:8]
		}
	}
	header := "\x1b[90m| Bạn"
	// One separator, between the author and the meta strip, and only when
	// the echo will draw that strip: /meta off shows neither the number nor
	// the badge. The number is unknown until the server assigns it, so the
	// strip carries only what is already known, and ⏳ marks the row pending.
	if pmMeta {
		header += " •"
		if badgePlaceholder != "" {
			header += " ✍️ ◆ " + badgePlaceholder
		}
	}
	header += " ⏳\x1b[0m"
	e.header = header

	// The text hangs under the header, one indented row per rendered line,
	// matching the layout the echo uses.
	var body strings.Builder
	for i, line := range lines {
		if i > 0 {
			body.WriteString("\n")
		}
		fmt.Fprintf(&body, "\x1b[90m%s%s\x1b[0m", chatBodyPrefix, markup.Linkify(line))
	}
	e.body = body.String()

	// The block takes over the input echo's slot, so the row the person
	// submitted becomes the row that says the message is on its way. A slot
	// that no longer holds the echo (evicted, or shifted by a repaint we
	// cannot see) falls back to appending, which keeps the block visible.
	// The returned slot names the placeholder, which is what the echo will
	// replace: reporting the echo's entry here would make every later
	// replace fail its identity check.
	next := slotRef{index: slot.index, entry: e}
	if !s.replaceSlot(slot, e, phShown) {
		s.emitTab(TabChat, e)
		next = slotRef{index: len(s.Display.TabChat.lines) - 1, entry: e}
	}
	s.refreshTerminal()
	return e, next, phShown
}

// sendMessage signs (trip) or envelopes (plain) one message, tracks its
func (s *Session) sendMessage(text string, placeholder entry, slot slotRef, phShown bool) error {
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
		err = s.sendJSON(tripMsg)
		if err != nil {
			// Rollback seq/prev on send failure to avoid permanent fork
			s.TripSeq--
			copy(s.TripPrev, prevCopy)
			s.Pending.TmpSeq--
		}
	} else {
		// Unsigned chat always travels in an envelope carrying the
		// session counter; raw text is rejected by the server.
		err = s.sendJSON(PlainMessage{TmpID: s.Pending.TmpSeq, Text: text, ReplyTo: s.Pending.PendingReplyTo})
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
		pm := pendingMsg{text: text, slot: slot, shown: phShown, epoch: s.Display.LayoutEpoch, sentAt: time.Now(), tmpID: s.Pending.TmpSeq, replyTo: s.Pending.PendingReplyTo}
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
			// The raced echo is consumed here: record its ID so a
			// later duplicate is dropped instead of stashed.
			s.Pending.SeenTmpIDs = noteConsumedTmpID(s.Pending.SeenTmpIDs, pm.tmpID)
			for i, p := range s.Pending.PendingPlaceholders {
				if p.tmpID == pm.tmpID {
					s.Pending.PendingPlaceholders = append(s.Pending.PendingPlaceholders[:i], s.Pending.PendingPlaceholders[i+1:]...)
					break
				}
			}
		}
		// Queue done: release before the erase, which stays on
		// DisplayMu (still held throughout, so no interleave).
		s.Pending.Mu.Unlock()
		if haveStashed {
			s.erasePlaceholderLocked(pm)
		}
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

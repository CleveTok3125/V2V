package main

import (
	"bytes"
	"fmt"
	"strings"
)

// Session display funnel (moved from main): tab buffers plus the single
// local-output path. Callers hold DisplayMu, except switchTab which
// takes it.

const (
	// outBufCap bounds deferred terminal output: beyond it the oldest
	// rendered chunks drop at line boundaries (counted in OutSkipped).
	// Their data stays in the tab buffers, so switching tabs replays
	// them. This keeps a stalled terminal from growing memory while
	// the socket read loop keeps draining.
	outBufCap = 512 * 1024
)

// enqueueOutput appends one ordered chunk to the terminal queue and
// wakes the flusher; it never blocks on the terminal. The caller may
// hold DisplayMu (the flusher takes only OutMu, a leaf lock).
func (s *Session) enqueueOutput(chunk string) {
	s.enqueueOutputKeep(chunk, false)
}

// enqueueOutputKeep is enqueueOutput with a cap exemption for one-off
// user-requested views (a full tab dump): those are already bounded by
// the tab buffer cap and must replay faithfully.
//
// The cap trims at line boundaries so drops never split a rendered
// line. A chunk with no newline at all (a placeholder wipe or erase
// escape sequence) cannot be trimmed that way; once such chunks alone
// exceed the cap the oldest is dropped whole, so a pathological burst
// of escapes still bounds memory.
func (s *Session) enqueueOutputKeep(chunk string, keep bool) {
	if chunk == "" {
		return
	}
	s.Display.OutMu.Lock()
	defer s.Display.OutMu.Unlock()
	s.Display.OutBuf = append(s.Display.OutBuf, chunk...)
	if keep {
		if !s.Display.OutFlushing {
			s.Display.OutFlushing = true
			go s.drainOutput()
		}
		return
	}
	for len(s.Display.OutBuf) > outBufCap {
		i := bytes.IndexByte(s.Display.OutBuf, '\n')
		if i >= 0 && i+1 < len(s.Display.OutBuf) {
			dropped := i + 1
			copy(s.Display.OutBuf, s.Display.OutBuf[dropped:])
			s.Display.OutBuf = s.Display.OutBuf[:len(s.Display.OutBuf)-dropped]
			s.Display.OutSkipped++
			continue
		}
		// No usable line boundary anywhere: keep the newest outBufCap
		// bytes so a burst of newline-free escape chunks still bounds
		// memory. This is the pathological path; it may split an
		// escape sequence, which only affects that skipped render.
		excess := len(s.Display.OutBuf) - outBufCap
		copy(s.Display.OutBuf, s.Display.OutBuf[excess:])
		s.Display.OutBuf = s.Display.OutBuf[:outBufCap]
		s.Display.OutSkipped++
	}
	if !s.Display.OutFlushing {
		s.Display.OutFlushing = true
		go s.drainOutput()
	}
}

// drainOutput writes queued terminal output outside the display lock.
// Exactly one instance runs at a time (guarded by OutFlushing under
// OutMu). The swap and its write are atomic under OutWriteMu, so a
// concurrent flushOutputNow can never interleave a newer batch ahead
// of one already taken; the empty-check and flag clear share the same
// locked section, so a chunk enqueued across the boundary either lands
// in this batch or spawns a fresh drainer.
func (s *Session) drainOutput() {
	for {
		s.Display.OutWriteMu.Lock()
		s.Display.OutMu.Lock()
		if len(s.Display.OutBuf) == 0 {
			s.Display.OutFlushing = false
			s.Display.OutMu.Unlock()
			s.Display.OutWriteMu.Unlock()
			return
		}
		batch := s.Display.OutBuf
		s.Display.OutBuf = nil
		s.Display.OutMu.Unlock()
		s.Display.Out.Write(batch)
		s.Display.OutWriteMu.Unlock()
	}
}

// flushOutputNow writes all queued terminal output synchronously and
// returns once the queue is empty. OutWriteMu is held throughout, so
// it never races a drainer's swap+write and batch order is preserved.
// Producers may append while it runs; the loop drains those too.
func (s *Session) flushOutputNow() {
	s.Display.OutWriteMu.Lock()
	defer s.Display.OutWriteMu.Unlock()
	for {
		s.Display.OutMu.Lock()
		batch := s.Display.OutBuf
		s.Display.OutBuf = nil
		s.Display.OutFlushing = false
		s.Display.OutMu.Unlock()
		if len(batch) == 0 {
			return
		}
		s.Display.Out.Write(batch)
	}
}

// refreshTerminal redraws the prompt after draining queued output. The two
// writers must not interleave: a block reaches the terminal as bytes appended
// to OutBuf and written by a drainer goroutine, while Refresh writes the
// prompt directly from the caller. Refreshing with a block still queued puts
// the prompt inside that block's byte sequence, which is how a notice or a
// prompt ends up wedged between a message's lines. Draining first makes the
// block whole on screen before anything else is drawn.
func (s *Session) refreshTerminal() {
	if s.Display.Sink != nil {
		s.Display.Sink.Notify()
		return
	}
	s.flushOutputNow()
	s.Display.Term.Refresh()
}

// holdOrEnqueue routes one rendered line to the terminal, or to the
// catch-up hold buffer while a join replay is being merged. Caller
// must hold DisplayMu.
func (s *Session) holdOrEnqueue(line string) {
	s.holdOrEnqueueChunk(heldChunk{text: line})
}

// holdOrEnqueueChunk is holdOrEnqueue for a piece that knows the chain height
// it reports, so a recovered block can be spliced into the held stream at its
// position instead of being appended.
func (s *Session) holdOrEnqueueChunk(chunk heldChunk) {
	if s.Display.Sink != nil {
		// The app paints from the buffers, so there is no deferred
		// terminal output to hold or enqueue.
		return
	}
	if s.Display.CatchupHold {
		s.Display.HoldLines = append(s.Display.HoldLines, chunk)
		return
	}
	s.enqueueOutput(chunk.text)
}

// releaseCatchupLocked prints everything accumulated during a held
// catch-up in one ordered chunk and clears the hold. Caller must hold
// DisplayMu.
func (s *Session) releaseCatchupLocked() {
	if !s.Display.CatchupHold {
		return
	}
	s.Display.CatchupHold = false
	s.Display.HoldGen++
	if s.Display.HoldTimer != nil {
		s.Display.HoldTimer.Stop()
		s.Display.HoldTimer = nil
	}
	if len(s.Display.HoldLines) > 0 {
		var sb strings.Builder
		for _, c := range s.Display.HoldLines {
			sb.WriteString(c.text)
		}
		s.enqueueOutput(sb.String())
		s.Display.HoldLines = nil
	}
	// The welcome line lands after the history it was waiting behind.
	s.flushPendingGreetingLocked()
}

// flushPendingGreetingLocked prints the held welcome line unless a
// catch-up is still running (then it waits for that release). Caller
// must hold DisplayMu.
func (s *Session) flushPendingGreetingLocked() {
	if s.Display.PendingGreeting == "" || s.Display.CatchupHold {
		return
	}
	greeting := s.Display.PendingGreeting
	s.Display.PendingGreeting = ""
	if s.Display.Sink != nil {
		e := noticeEntry(strings.Trim(greeting, "\n"))
		s.Display.TabChat.append(e)
		s.Display.TabSys.append(e)
		s.Display.Sink.Notify()
		return
	}
	s.enqueueOutput(greeting)
}

func (s *Session) emitTab(tab int, e entry) {
	s.emitTabLive(true, tab, e)
}

// olderAnchor is where an older window splices into a buffer: just above the
// oldest buffered message, so the startup notices stay at the top instead of
// being pushed under the older history. Falls back to the front when the
// buffer holds no chained message yet.
func olderAnchor(buf *tabBuffer) int {
	if buf == nil {
		return 0
	}
	for i, e := range buf.lines {
		if e.height > 0 || e.banner {
			return i
		}
	}
	return 0
}

// placeEntry writes e into its tab buffer. During an on-demand older window
// its wires predate everything buffered, so they are spliced in at the anchor
// (in arrival order) instead of appended; every other line appends. Caller
// must hold DisplayMu.
func (s *Session) placeEntry(tab int, e entry) {
	if tab == TabSystem {
		if s.Chain.InOlder {
			s.Display.TabSys.insertAt(s.Chain.OlderInsertSys, []entry{e})
			s.Chain.OlderInsertSys++
			return
		}
		s.Display.TabSys.append(e)
		return
	}
	if s.Chain.InOlder {
		s.Display.TabChat.insertAt(s.Chain.OlderInsert, []entry{e})
		s.Chain.OlderInsert++
		return
	}
	s.Display.TabChat.append(e)
}

// emitTabLive appends a line to its tab buffer and prints it live only
// when live is true. A muted line still lands in the buffer, so it
// stays reviewable in the system tab and on tab switches. Caller must
// hold DisplayMu.
func (s *Session) emitTabLive(live bool, tab int, e entry) {
	if s.Display.Sink != nil {
		// Tab 1 is the full stream (chat plus local and system lines, in
		// arrival order); tab 2 is system-only.
		s.placeEntry(TabChat, e)
		if tab == TabSystem {
			s.placeEntry(TabSystem, e)
		}
		s.Display.Sink.Notify()
		return
	}
	s.placeEntry(tab, e)
	if !live {
		return
	}
	// Tab 1 shows the full legacy stream, so it is unaffected by tabs.
	// Tab 2 is purely additive and shows only its own lines.
	if tab == s.Display.ActiveTab || s.Display.ActiveTab == TabChat {
		s.holdOrEnqueueChunk(heldChunk{text: e.text(), height: e.height})
		s.Display.PrintGen++
	}
}

// emitLocalFeedback is the single funnel for all local output: it
// buffers the line in TabSystem and queues it for the active tab
// immediately, so commands always respond visibly while staying
// reviewable in Tab 2. Every current and future local message must
// go through here — printing to out directly bypasses Tab 2 and
// leaves it incomplete. Caller must hold DisplayMu.
func (s *Session) emitLocalFeedback(line string) {
	s.emitLocalFeedbackLive(true, line)
}

// clearInputRow erases the row readline was editing and leaves the cursor at
// its start. That row is never a buffer entry, so no repaint accounts for it.
// Caller must not hold DisplayMu.
func (s *Session) clearInputRow() {
	s.Display.DisplayMu.Lock()
	defer s.Display.DisplayMu.Unlock()
	s.holdOrEnqueue("\r\x1b[2K")
}

// slotRef is a buffer entry a later write replaces in place: where it sits,
// and the entry itself so the replace can confirm the slot still holds what
// the caller wrote.
type slotRef struct {
	index int
	entry entry
}

// appendEntrySlot appends one entry and prints it, returning the slot it
// occupies. A caller keeps the slot to replace the entry later, which is how
// one message moves from the input echo to its pending block to its server
// echo without ever leaving a second row behind. Caller must hold DisplayMu.
func (s *Session) appendEntrySlot(live bool, tab int, e entry) slotRef {
	s.emitTabLive(live, tab, e)
	buf := s.Display.TabChat
	if tab != TabChat {
		buf = s.Display.TabSys
	}
	return slotRef{index: len(buf.lines) - 1, entry: e}
}

// entriesRows is the terminal rows a run of entries occupies.
func entriesRows(entries []entry, width int) int {
	total := 0
	for _, e := range entries {
		total += e.rows(width)
	}
	return total
}

// replaceSlot rewrites one buffer entry and, when repaint is true, repaints
// from that entry down: erasing to the end of the screen takes the entries
// after it with it, so they are printed again. repaint is false when the
// cursor is no longer under the block — a tab switch moved it — and the
// buffer then updates alone, the screen catching up on the next repaint.
// Reports false when the slot no longer holds what the caller wrote, leaving
// the buffer untouched so the caller can fall back to appending. Caller must
// hold DisplayMu.
func (s *Session) replaceSlot(slot slotRef, next entry, repaint bool) bool {
	lines := s.Display.TabChat.lines
	if slot.index < 0 || slot.index >= len(lines) || lines[slot.index] != slot.entry {
		return false
	}
	tail := append([]entry{}, lines[slot.index+1:]...)
	lines[slot.index] = next
	if s.Display.Sink != nil {
		s.Display.Sink.Notify()
		return true
	}
	if !repaint {
		return true
	}
	width := termWidth()
	var sb strings.Builder
	fmt.Fprintf(&sb, "\x1b[%dA", slot.entry.rows(width)+entriesRows(tail, width))
	sb.WriteString("\x1b[J")
	sb.WriteString(next.text())
	for _, e := range tail {
		sb.WriteString(e.text())
	}
	s.holdOrEnqueue(sb.String())
	s.Display.PrintGen++
	return true
}

// noticeEntry is a locally generated line as a buffer entry: one header row,
// no chain position and no body.
func noticeEntry(line string) entry {
	return entry{header: strings.TrimSuffix(line, "\n"), kind: kindNotice}
}

// systemEntry is a server system line as a buffer entry.
func systemEntry(line string) entry {
	return entry{header: strings.TrimSuffix(line, "\n"), kind: kindSystem}
}

// emitLocalFeedbackTags is emitLocalFeedback for informational notices
// that /notify can mute: the line always lands in Tab 2, but the live
// print is skipped while any of its tags is muted. Caller must hold
// DisplayMu.
func (s *Session) emitLocalFeedbackTags(tags []string, line string) {
	s.emitLocalFeedbackLive(s.notifyTagsAllowed(tags), line)
}

// emitLocalFeedbackLive buffers a local line in TabSystem and prints it
// live only when live is true; a muted line remains reviewable in Tab 2.
// Caller must hold DisplayMu.
func (s *Session) emitLocalFeedbackLive(live bool, line string) {
	e := noticeEntry(line)
	if s.Display.Sink != nil {
		s.Display.TabChat.append(e)
		s.Display.TabSys.append(e)
		s.Display.Sink.Notify()
		return
	}
	s.Display.TabSys.append(e)
	if !live {
		return
	}
	s.holdOrEnqueueChunk(heldChunk{text: e.text(), height: e.height})
	s.Display.PrintGen++
}

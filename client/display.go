package main

import (
	"strings"
)

// Session display funnel (moved from main): tab buffers plus the single
// local-output path. Callers hold DisplayMu, except switchTab which
// takes it.

// refreshTerminal asks the app to repaint from the buffers.
func (s *Session) refreshTerminal() {
	if s.Display.Sink != nil {
		s.Display.Sink.Notify()
	}
}

// releaseCatchupLocked ends a held catch-up and flushes the held welcome line.
// Caller must hold DisplayMu.
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
	s.flushPendingGreetingLocked()
}

// flushPendingGreetingLocked lands the held welcome line unless a catch-up is
// still running (then it waits for that release). Caller must hold DisplayMu.
func (s *Session) flushPendingGreetingLocked() {
	if s.Display.PendingGreeting == "" || s.Display.CatchupHold {
		return
	}
	greeting := s.Display.PendingGreeting
	s.Display.PendingGreeting = ""
	e := noticeEntry(strings.Trim(greeting, "\n"))
	s.Display.TabChat.append(e)
	s.Display.TabSys.append(e)
	if s.Display.Sink != nil {
		s.Display.Sink.Notify()
	}
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

// emitTabLive appends a line to its tab buffer. Tab 1 is the full stream (chat
// plus local and system lines, in arrival order); tab 2 is system-only. live
// gates the chat tab, so a muted notice stays reviewable in tab 2 only.
// Caller must hold DisplayMu.
func (s *Session) emitTabLive(live bool, tab int, e entry) {
	e.muted = !live
	s.placeEntry(TabChat, e)
	// A muted line is hidden in the chat paint, so it also lands in the
	// system tab to stay reviewable.
	if tab == TabSystem || e.muted {
		s.placeEntry(TabSystem, e)
	}
	if s.Display.Sink != nil {
		s.Display.Sink.Notify()
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

// replaceSlot rewrites one buffer entry. Reports false when the slot no longer
// holds what the caller wrote, leaving the buffer untouched so the caller can
// fall back to appending. Caller must hold DisplayMu.
func (s *Session) replaceSlot(slot slotRef, next entry) bool {
	lines := s.Display.TabChat.lines
	if slot.index < 0 || slot.index >= len(lines) || lines[slot.index] != slot.entry {
		return false
	}
	lines[slot.index] = next
	if s.Display.Sink != nil {
		s.Display.Sink.Notify()
	}
	return true
}

// noticeEntry is a locally generated line as a buffer entry: one header row,
// no chain position and no body. The box draws its own frame, so the text
// is stored as is.
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

// emitLocalFeedbackLive buffers a local line. A muted line lands in Tab 2 only,
// so it stays reviewable without showing in the chat stream. Caller must hold
// DisplayMu.
func (s *Session) emitLocalFeedbackLive(live bool, line string) {
	s.emitLocalEntry(live, noticeEntry(line))
}

// emitStatusLocked buffers a line about the buffer itself, such as a copy
// confirmation. It is printed and buffered like any local line, but never
// becomes a copy source. Caller must hold DisplayMu.
func (s *Session) emitStatusLocked(line string) {
	e := noticeEntry(line)
	e.status = true
	s.emitLocalEntry(true, e)
}

// emitLocalEntry writes one local entry into both buffers. Caller must hold
// DisplayMu.
func (s *Session) emitLocalEntry(live bool, e entry) {
	e.muted = !live
	s.Display.TabChat.append(e)
	s.Display.TabSys.append(e)
	if s.Display.Sink != nil {
		s.Display.Sink.Notify()
	}
}

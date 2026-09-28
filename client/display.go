package main

import (
	"bytes"
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

func (s *Session) emitTab(tab int, line string) {
	if tab == TabChat {
		s.Display.TabChat.append(line)
	} else {
		s.Display.TabSys.append(line)
	}
	// Tab 1 shows the full legacy stream, so it is unaffected by tabs.
	// Tab 2 is purely additive and shows only its own lines.
	if tab == s.Display.ActiveTab || s.Display.ActiveTab == TabChat {
		s.enqueueOutput(line)
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
	s.Display.TabSys.append(line)
	s.enqueueOutput(line)
	s.Display.PrintGen++
}

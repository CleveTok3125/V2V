package main

import (
	"os"
	"strings"
)

// histMax bounds the recall list; older entries drop from the front.
const histMax = 1000

// recordHistory offers a submitted line to the app's recall list. Commands
// always belong there; a chat message only when ui.history.saveMessages asks
// for it, so private text stays out of the on-disk history by default. The
// decision comes from what dispatch did, never from a prefix, so a message
// that happens to start with "/" is still a message.
func (s *Session) recordHistory(line string, isCommand bool) {
	if s.Display.Sink == nil {
		return
	}
	if !isCommand && (ClientCfg == nil || !ClientCfg.SaveMessageHistory()) {
		return
	}
	s.Display.Sink.RecordHistory(line)
}

// inputHistory is the arrow-up/down recall list. It persists to the same
// file readline used (historyFile) so /clearhistory keeps its meaning.
// The newest entry is last; idx == len(lines) means "editing a fresh
// draft", and draft holds that in-progress line while browsing.
type inputHistory struct {
	lines []string
	idx   int
	draft string
	path  string
}

// loadHistory reads the persisted lines, best effort: a missing or
// unreadable file (read-only cache dir, first run) yields an empty list.
func loadHistory(path string) *inputHistory {
	h := &inputHistory{path: path}
	if path == "" {
		return h
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return h
	}
	for _, line := range strings.Split(string(data), "\n") {
		if line = strings.TrimRight(line, "\r"); line != "" {
			h.lines = append(h.lines, line)
		}
	}
	if len(h.lines) > histMax {
		h.lines = h.lines[len(h.lines)-histMax:]
	}
	h.idx = len(h.lines)
	return h
}

// add records a submitted line, skipping an immediate duplicate, and
// persists. Callers pass the raw draft; a trailing newline is dropped so a
// multi-line send still stores one entry. The session decides what belongs
// here (see Session.recordHistory), so this stores whatever it is given.
func (h *inputHistory) add(line string) {
	line = strings.TrimRight(line, "\n")
	if line == "" {
		return
	}
	if n := len(h.lines); n > 0 && h.lines[n-1] == line {
		h.idx = len(h.lines)
		return
	}
	h.lines = append(h.lines, line)
	if len(h.lines) > histMax {
		h.lines = h.lines[len(h.lines)-histMax:]
	}
	h.idx = len(h.lines)
	h.save()
}

// prev steps to the older entry, stashing the current draft on the first
// step. Reports false when already at the oldest entry.
func (h *inputHistory) prev(draft string) (string, bool) {
	if h.idx <= 0 || len(h.lines) == 0 {
		return "", false
	}
	if h.idx == len(h.lines) {
		h.draft = draft
	}
	h.idx--
	return h.lines[h.idx], true
}

// next steps back toward the draft, restoring it at the end.
func (h *inputHistory) next() (string, bool) {
	if h.idx >= len(h.lines) {
		return "", false
	}
	h.idx++
	if h.idx == len(h.lines) {
		return h.draft, true
	}
	return h.lines[h.idx], true
}

// save rewrites the file owner-only (0600). Failures are ignored: the
// cache dir may be read-only, and history is a convenience, not state.
func (h *inputHistory) save() {
	if h.path == "" {
		return
	}
	f, err := os.OpenFile(h.path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return
	}
	_, _ = f.WriteString(strings.Join(h.lines, "\n") + "\n")
	_ = f.Close()
	// An existing file keeps its old mode across O_TRUNC, so tighten it.
	_ = os.Chmod(h.path, 0o600)
}

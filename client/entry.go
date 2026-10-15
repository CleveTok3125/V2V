package main

import "strings"

// entry is one buffer unit: a message and every display part that belongs to
// it. The parts are fields rather than rows to be parsed back apart, so
// nothing has to search rendered text to find a body, a chain position, or the
// unit it wrote.
type entry struct {
	quote  string // rendered quote rows, no trailing newline; "" when none
	header string // first row: author and chain, or the whole line for a notice
	body   string // rendered body rows, no trailing newline; "" when none

	// Identity, so a lookup never scans text.
	height    uint64 // chain height this entry reports; 0 when it has none
	hashHex   string // short hash as printed, "" when none
	collapsed bool   // body was folded, so /expand targets this entry
	banner    bool   // a date banner row, so it can anchor older windows
	day       string // sys_date of a banner, "" otherwise
	kind      int    // box border colour: kindChat, kindLocal, kindSystem
	quoteKind int    // border colour of the nested quote block, same scale
	// quoteEntry, when set, is the block nested inside this one: it renders
	// with its own frame, structure and style instead of a plain quote row.
	quoteEntry *entry
	muted      bool // muted notice: buffered for the system tab, hidden in chat
}

// Entry box kinds pick the border colour and the side of the viewport.
const (
	kindChat   = iota // a chat wire from anyone else: left, blue
	kindMine          // our own echoed chat wire: right, blue
	kindLocal         // our pending echo: right, grey
	kindNotice        // a local feedback line: left, grey
	kindSystem        // a server system line: left, banana yellow
)

// rightAligned reports whether the entry sits on the right of the viewport:
// our own messages, pending or echoed.
func (e entry) rightAligned() bool {
	return e.kind == kindMine || e.kind == kindLocal
}

// entryGapRow is the blank row that separates one entry from the next. It
// rides inside the entry, so a repaint, a slot replacement and the erase math
// all carry it without anything having to remember what was drawn last.
const entryGapRow = "\n"

// text is the exact bytes this entry contributes to the screen and to a tab
// repaint: the parts that exist, one row each, plus the blank row that sets
// this entry apart from the next. An empty entry contributes nothing at all,
// so it never leaves a stray blank behind.
func (e entry) text() string {
	var sb strings.Builder
	if e.quoteEntry != nil {
		sb.WriteString(strings.TrimRight(e.quoteEntry.text(), "\n"))
		sb.WriteString("\n")
	}
	for _, part := range [...]string{e.quote, e.header, e.body} {
		if part == "" {
			continue
		}
		sb.WriteString(part)
		sb.WriteString("\n")
	}
	if sb.Len() > 0 {
		sb.WriteString(entryGapRow)
	}
	return sb.String()
}

// rows is how many terminal rows the entry occupies at the given width.
// matchesHeight reports whether this entry is the one that reports h, with an
// optional hash prefix to tell two entries at the same height apart.
func (e entry) matchesHeight(h uint64, hashPrefix string) bool {
	// Zero means the entry reports no chain position at all, so it can never
	// answer for a height.
	if e.height == 0 || e.height != h {
		return false
	}
	if hashPrefix == "" {
		return true
	}
	return strings.HasPrefix(strings.ToLower(e.hashHex), strings.ToLower(hashPrefix))
}

package main

// Long-message folding: blocks taller than CollapseRows screen rows
// render as preview rows plus an inline expand trailer. Row math counts
// characters, lines and terminal wrap (CJK-aware via cells.go) against
// the live width, falling back to 80 columns off-TTY.

import (
	"fmt"
	"os"
	"strings"

	xterm "github.com/charmbracelet/x/term"
)

// termWidth returns live terminal columns, 80 when undetectable.
func termWidth() int {
	if w, _, err := xterm.GetSize(os.Stdout.Fd()); err == nil && w > 0 {
		return w
	}
	return 80
}

// estimateRows counts screen rows for s: every logical line costs at
// least one row plus wrapped overflow. Trailing newline adds no row.
func estimateRows(s string, width int) int {
	if width < 1 {
		width = 80
	}
	rows := 0
	for _, line := range strings.Split(strings.TrimSuffix(s, "\n"), "\n") {
		rows += rowCost(line, width)
	}
	return rows
}

// rowCost is screen rows for one logical line (empty line costs 1).
func rowCost(line string, width int) int {
	if cells := runeStrCells(line); cells > 0 {
		return (cells + width - 1) / width
	}
	return 1
}

// cutCells returns the longest rune prefix of s fitting in budget
// cells (wide runes never split).
func cutCells(s string, budget int) string {
	cells := 0
	for i, r := range s {
		cells += runeWidth(r)
		if cells > budget {
			return s[:i]
		}
	}
	return s
}

// expandMarker opens the trailer's command bracket. The renderer and the
// click handler both read it from here so they cannot drift apart. Spaces
// inside are non-breaking so word-wrap keeps the bracket on one row: a
// bracket split across rows loses hover and click.
const expandMarker = "[Xem\u00a0thêm:\u00a0/expand\u00a0#"

// expandTrailer renders the inline trailer addressing a collapsed block: dim
// "..." plus the expand command. The app draws the bracket as a button on
// every platform, so it carries no link of its own.
func expandTrailer(height uint64) string {
	return dimStyle.Render("...") + " " + fmt.Sprintf("%s%d]", expandMarker, height)
}

// collapsedIndex locates the folded entry for height, or -1. The entry
// records that it was folded, so nothing reads the expand trailer back out of
// the rendered rows.
func collapsedIndex(lines []entry, height uint64) int {
	for i, e := range lines {
		if e.collapsed && e.height == height {
			return i
		}
	}
	return -1
}

// foldLines walks lines once, emitting whole lines while they fit in
// budget rows. A single wall taller than the whole budget is cut
// mid-line instead (line boundaries cannot apply when one line owns
// every row). Returns emitted lines, possibly empty.
func foldLines(lines []string, budget, width int) []string {
	var kept []string
	used := 0
	for _, line := range lines {
		if used+rowCost(line, width) > budget {
			if len(kept) == 0 {
				kept = append(kept, cutCells(line, budget*width))
			}
			break
		}
		used += rowCost(line, width)
		kept = append(kept, line)
	}
	return kept
}

// collapseBody folds a body to preview rows with the expand trailer on the
// last kept row. The header is not touched: folding it away would leave a body
// with nobody attached to it. False means the body fits untouched.
func collapseBody(body string, height uint64, rows, preview, width int) (string, bool) {
	if height == 0 || body == "" {
		return body, false
	}
	lines := strings.Split(body, "\n")
	if estimateRows(body, width) <= rows {
		return body, false
	}
	kept := foldLines(lines, preview, width)
	if len(kept) == 0 {
		return body, false // unreachable: budget fits ≥1 row
	}
	kept[len(kept)-1] += expandTrailer(height)
	return strings.Join(kept, "\n"), true
}

// maybeCollapse folds head when it exceeds the configured row budget.
// System-tab content and height-less wires render untouched; everything
// else collapses. Pure except the live width probe.
func maybeCollapse(e entry, wire WireMessage, tab int) entry {
	if tab == TabSystem || ClientCfg == nil || !ClientCfg.CollapseEnabled() {
		return e
	}
	folded, ok := collapseBody(e.body, wire.ChainHeight, ClientCfg.CollapseRows(), ClientCfg.CollapsePreviewRows(), termWidth())
	if !ok {
		return e
	}
	e.body = folded
	e.collapsed = true
	return e
}

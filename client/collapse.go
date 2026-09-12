package main

// Long-message folding: blocks taller than CollapseRows screen rows
// render as preview rows plus an inline expand trailer. Row math counts
// characters, lines and terminal wrap (CJK-aware via cells.go) against
// the live width, falling back to 80 columns off-TTY.

import (
	"fmt"
	"os"
	"runtime"
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

// expandTrailer renders the inline trailer addressing a collapsed
// block: dim "..." plus the expand command. On web just the bracket
// part becomes an OSC8 link (desktop terminals cannot report clicks,
// so the ellipsis and the space stay outside the clickable region).
func expandTrailer(height uint64) string {
	cmd := fmt.Sprintf("[Xem thêm: /expand #%d]", height)
	tail := "\x1b[90m...\x1b[0m " + cmd
	if runtime.GOOS == "js" {
		tail = "\x1b[90m...\x1b[0m " + fmt.Sprintf("\x1b]8;;v2v://expand/%d\x1b\\%s\x1b]8;;\x1b\\", height, cmd)
	}
	return tail
}

// findCollapsed locates the buffer entry holding the expand trailer for
// height, or -1. The trailing "]" pins exact heights (#4 never matches
// #42's trailer).
func findCollapsed(lines []string, height uint64) int {
	marker := fmt.Sprintf("/expand #%d]", height)
	for i, line := range lines {
		if strings.Contains(line, marker) {
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

// collapseHead folds head to previewRows head rows with the expand
// trailer on the last preview line. False means head fits untouched.
func collapseHead(head string, height uint64, rows, preview, width int) (string, bool) {
	if height == 0 || estimateRows(head, width) <= rows {
		return head, false
	}
	kept := foldLines(strings.Split(strings.TrimSuffix(head, "\n"), "\n"), preview, width)
	if len(kept) == 0 {
		return head, false // unreachable: budget fits ≥1 row
	}
	kept[len(kept)-1] += expandTrailer(height)
	return strings.Join(kept, "\n") + "\n", true
}

// maybeCollapse folds head when it exceeds the configured row budget.
// System-tab content and height-less wires render untouched; everything
// else collapses. Pure except the live width probe.
func maybeCollapse(head string, wire WireMessage, tab int) string {
	if tab == TabSystem || ClientCfg == nil || !ClientCfg.CollapseEnabled() {
		return head
	}
	folded, _ := collapseHead(head, wire.ChainHeight, ClientCfg.CollapseRows(), ClientCfg.CollapsePreviewRows(), termWidth())
	return folded
}

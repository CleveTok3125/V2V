package main

// Long-message folding: blocks taller than CollapseRows screen rows
// render as head preview rows plus an expand trailer. Row math counts
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
		cells := runeStrCells(line)
		if cells == 0 {
			rows++
			continue
		}
		rows += (cells + width - 1) / width
	}
	return rows
}

// maybeCollapse folds head when it exceeds the configured row budget.
// System-tab content and height-less wires render untouched; everything
// else collapses. Pure except the live width probe.
func maybeCollapse(head string, wire WireMessage, tab int) string {
	if tab == TabSystem || ClientCfg == nil || !ClientCfg.CollapseEnabled() {
		return head
	}
	folded, ok := collapseHead(head, wire.ChainHeight, ClientCfg.CollapseRows(), ClientCfg.CollapsePreviewRows(), termWidth())
	if !ok || runtime.GOOS != "js" {
		return folded
	}
	// Web only: wrap just the trailer tail in an OSC8 link so a click
	// expands. Desktop terminals cannot report clicks on this stack.
	lines := strings.Split(strings.TrimSuffix(folded, "\n"), "\n")
	last := lines[len(lines)-1]
	if i := strings.LastIndex(last, "\x1b[90m..."); i >= 0 {
		lines[len(lines)-1] = last[:i] + fmt.Sprintf("\x1b]8;;v2v://expand/%d\x1b\\%s\x1b]8;;\x1b\\", wire.ChainHeight, last[i:])
	}
	return strings.Join(lines, "\n") + "\n"
}

// expandTrailer renders the inline trailer addressing a collapsed
// block: dim "..." plus a plain expand command. It carries no newline;
// collapseHead appends it to the last preview line.
func expandTrailer(height uint64) string {
	return fmt.Sprintf("\x1b[90m...\x1b[0m [Xem thêm: /expand #%d]", height)
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

// collapseHead folds head to previewRows head rows with the expand
// trailer appended to the last preview line (same line, never its own).
// Cut happens on line boundaries, never mid-line.
func collapseHead(head string, height uint64, rows, preview, width int) (string, bool) {
	trimmed := strings.TrimSuffix(head, "\n")
	lines := strings.Split(trimmed, "\n")
	if estimateRows(head, width) <= rows || height == 0 {
		return head, false
	}
	used := 0
	cut := 0
	partial := ""
	for i, line := range lines {
		cells := runeStrCells(line)
		cost := (cells + width - 1) / width
		if cost == 0 {
			cost = 1
		}
		if used+cost > preview {
			if cut == 0 {
				// Single wall taller than the whole budget: cut
				// mid-line at the remaining cells. Line boundaries
				// cannot apply when one line owns every row.
				budget := (preview - used) * width
				if budget < 1 {
					budget = width
				}
				partial = cutCells(line, budget)
			}
			break
		}
		used += cost
		cut = i + 1
	}
	var kept []string
	kept = append(kept, lines[:cut]...)
	if partial != "" {
		kept = append(kept, partial)
	}
	if len(kept) == 0 {
		return head, false // unreachable: budget always fits ≥1 line
	}
	// Trailer rides the last preview line, never its own.
	kept[len(kept)-1] += expandTrailer(height)
	return strings.Join(kept, "\n") + "\n", true
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

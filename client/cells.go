package main

// Display-cell arithmetic shared by every platform: terminals address the
// screen in cells, so wide/astral runes count as two columns while combining
// marks count as none. The WASM line editor relies on these helpers to keep
// prompts, drafts and the cursor aligned on wrapped multi-row lines.

import (
	"github.com/mattn/go-runewidth"
)

func lineCells(rs []rune) int {
	n := 0
	for _, r := range rs {
		n += runeWidth(r)
	}
	return n
}

func runeStrCells(s string) int {
	n := 0
	for _, r := range s {
		n += runeWidth(r)
	}
	return n
}

// widthCondition pins deterministic non-CJK cell widths: the package
// default follows the process locale, but display math must not change
// between machines. runeWidth keeps its old per-rune summation (not
// grapheme clustering), so only the width table itself is new.
var widthCondition = &runewidth.Condition{EastAsianWidth: false, StrictEmojiNeutral: true}

// runeWidth returns the display cell width of a rune (0 = zero-width,
// 1 = narrow, 2 = East Asian wide/fullwidth) via go-runewidth.
func runeWidth(r rune) int {
	return widthCondition.RuneWidth(r)
}

// editRowsWithin returns how many terminal rows sit between the block top
// and the cursor when the cursor is `cells` display cells into the block.
// A cell offset that lands exactly on a column boundary means the cursor
// rests at the END of the previous row (pending soft wrap), hence the -1.
func editRowsWithin(cells, cols int) int {
	if cols < 1 {
		cols = 80
	}
	r := cells / cols
	if cells > 0 && cells%cols == 0 {
		r--
	}
	if r < 0 {
		r = 0
	}
	return r
}

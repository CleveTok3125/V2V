package main

// Display-cell arithmetic shared by every platform: terminals address the
// screen in cells, so wide/astral runes count as two columns while combining
// marks count as none. The WASM line editor relies on these helpers to keep
// prompts, drafts and the cursor aligned on wrapped multi-row lines.

import "unicode"

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

// runeWidth returns the display cell width of a rune (0 = zero-width,
// 1 = narrow, 2 = East Asian wide/fullwidth). Approximation without pulling
// in an external width table.
func runeWidth(r rune) int {
	if r == 0 {
		return 1
	}
	if unicode.Is(unicode.Mn, r) || unicode.Is(unicode.Me, r) || unicode.Is(unicode.Cf, r) {
		return 0
	}
	switch {
	case r >= 0x1100 && r <= 0x115F, // Hangul Jamo
		r >= 0x2E80 && r <= 0x303E, // CJK Radicals .. CJK Symbols
		r >= 0x3041 && r <= 0x33FF, // Hiragana .. CJK Compatibility
		r >= 0x3400 && r <= 0x4DBF, // CJK Ext A
		r >= 0x4E00 && r <= 0x9FFF, // CJK Unified
		r >= 0xA000 && r <= 0xA4CF, // Yi
		r >= 0xAC00 && r <= 0xD7A3, // Hangul Syllables
		r >= 0xF900 && r <= 0xFAFF, // CJK Compat Ideographs
		r >= 0xFE30 && r <= 0xFE4F, // CJK Compat Forms
		r >= 0xFF00 && r <= 0xFF60, // Fullwidth Forms
		r >= 0xFFE0 && r <= 0xFFE6, // Fullwidth signs
		r > 0xFFFF:                 // astral (emoji, ...)
		return 2
	}
	return 1
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

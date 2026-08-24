package main

import "testing"

func TestRuneWidth(t *testing.T) {
	cases := []struct {
		r    rune
		want int
	}{
		{'a', 1},
		{'é', 1},    // single-rune precomposed
		{'ế', 1},    // base + width-1 (precomposed U+1EBF)
		{0x0301, 0}, // combining acute (Mn)
		{'中', 2},    // CJK
		{'가', 2},    // Hangul syllable
		{'Ａ', 2},    // fullwidth A
		{'😀', 2},    // astral emoji
		{0x200B, 0}, // zero-width space (Cf)
	}
	for _, c := range cases {
		if got := runeWidth(c.r); got != c.want {
			t.Errorf("runeWidth(%q) = %d, want %d", c.r, got, c.want)
		}
	}
}

func TestLineCells(t *testing.T) {
	if got := lineCells([]rune("| > ")); got != 4 {
		t.Errorf("prompt cells = %d, want 4", got)
	}
	// a(1) 中(2) b(1) = 4 cells from 3 runes
	if got := lineCells([]rune("a中b")); got != 4 {
		t.Errorf("a中b cells = %d, want 4", got)
	}
	// emoji counts as 2 even though it is one rune
	if got := lineCells([]rune("a😀b")); got != 4 {
		t.Errorf("a😀b cells = %d, want 4", got)
	}
}

func TestEditRowsWithin(t *testing.T) {
	cases := []struct {
		cells, cols, want int
		note              string
	}{
		{0, 10, 0, "empty block"},
		{5, 10, 0, "first row interior"},
		{10, 10, 0, "boundary: cursor rests at end of row 0"},
		{11, 10, 1, "one cell into row 1"},
		{20, 10, 1, "boundary at end of row 1"},
		{21, 10, 2, "row 2 interior"},
		{80, 80, 0, "default cols boundary"},
		{81, 80, 1, "default cols past boundary"},
		{7, 0, 0, "degenerate cols falls back to 80"},
	}
	for _, c := range cases {
		if got := editRowsWithin(c.cells, c.cols); got != c.want {
			t.Errorf("editRowsWithin(%d, %d) [%s] = %d, want %d",
				c.cells, c.cols, c.note, got, c.want)
		}
	}
}

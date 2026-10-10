package main

import (
	"strings"
	"testing"

	"github.com/CleveTok3125/V2V/internal/config"
)

func TestEstimateRows(t *testing.T) {
	if got := estimateRows("hi\n", 80); got != 1 {
		t.Fatalf("single line = %d, want 1", got)
	}
	if got := estimateRows("a\nb\nc\n", 80); got != 3 {
		t.Fatalf("3 lines = %d, want 3", got)
	}
	if got := estimateRows("a\n\nb\n", 80); got != 3 {
		t.Fatalf("empty middle line must cost a row, got %d", got)
	}
	// 200 cells at width 80 wrap to 3 rows (ceil(200/80)).
	long := strings.Repeat("x", 200)
	if got := estimateRows(long+"\n", 80); got != 3 {
		t.Fatalf("wrapped line = %d, want 3", got)
	}
	// CJK wide runes count double: 50 wide chars = 100 cells.
	wide := strings.Repeat("あ", 50)
	if got := estimateRows(wide+"\n", 80); got != 2 {
		t.Fatalf("wide line = %d, want 2", got)
	}
	if got := estimateRows("x", 0); got != 1 {
		t.Fatalf("zero width must fall back, got %d", got)
	}
}

func TestCollapseHead(t *testing.T) {
	const header = "| 15:04 Alice • #42:abcd"
	// mk builds n body rows; a block is that body under one author header.
	mk := func(n int) string {
		var b strings.Builder
		for i := 1; i <= n; i++ {
			b.WriteString(strings.Repeat("l", 10))
			b.WriteByte('\n')
		}
		return b.String()
	}
	blk := func(body string) string { return header + "\n" + body }

	// Body fits: untouched, no trailer.
	if out, folded := collapseHead(blk(mk(10)), 42, 10, 5, 80); folded || out != blk(mk(10)) {
		t.Fatal("10 body rows at threshold must not fold")
	}
	// 11 body rows fold to 5 preview rows with the trailer on the last one,
	// and the header stays: folding it away would leave the body unattached.
	out, folded := collapseHead(blk(mk(11)), 42, 10, 5, 80)
	if !folded {
		t.Fatal("11 body rows must fold")
	}
	lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	if len(lines) != 6 {
		t.Fatalf("folded block = %d lines, want header + 5 preview", len(lines))
	}
	if lines[0] != header {
		t.Fatalf("the header must survive folding, got %q", lines[0])
	}
	if !strings.Contains(lines[5], "/expand #42") {
		t.Fatalf("trailer must address height on the last preview row, got %q", lines[5])
	}
	// Height-less wires never fold (no /expand address exists).
	if _, folded := collapseHead(blk(mk(30)), 0, 10, 5, 80); folded {
		t.Fatal("height 0 must never fold")
	}
	// A header with no body has nothing to fold.
	if _, folded := collapseHead(header+"\n", 42, 10, 5, 80); folded {
		t.Fatal("a lone header must not fold")
	}
	// Wrapped long single line folds by screen rows AND cuts content:
	// 900 cells at width 80 must not survive verbatim.
	out, folded = collapseHead(blk(strings.Repeat("y", 900)+"\n"), 7, 10, 5, 80)
	if !folded {
		t.Fatal("900 cells at width 80 (12 rows) must fold")
	}
	if strings.Contains(out, strings.Repeat("y", 900)) {
		t.Fatal("single wall survived unfolded")
	}
	// header + the cut wall's 5 preview rows + the trailer wrapping onto a
	// 6th.
	if got := estimateRows(out, 80); got > 7 {
		t.Fatalf("folded wall = %d rows, want ≤ header + preview + wrapped trailer", got)
	}
	if !strings.Contains(out, "/expand #7]") {
		t.Fatalf("folded wall must carry trailer, got %q", out[len(out)-60:])
	}
	// Mid-line cut never splits a wide rune: 500 あ (1000 cells = 13
	// rows at width 80) with preview 5 → 400 cells kept, all complete.
	out, folded = collapseHead(blk(strings.Repeat("あ", 500)+"\n"), 8, 10, 5, 80)
	if !folded {
		t.Fatal("wide wall must fold")
	}
	if got := strings.Count(out, "あ"); got != 200 {
		t.Fatalf("mid-line cut kept %d wide runes, want 200", got)
	}
	// Cut happens on line boundaries: preview rows respected.
	out, _ = collapseHead(blk(mk(30)), 9, 10, 5, 80)
	if got := len(strings.Split(strings.TrimSuffix(out, "\n"), "\n")); got != 6 {
		t.Fatalf("preview cut = %d lines, want header + 5 preview", got)
	}
}

func TestMaybeCollapse(t *testing.T) {
	oldCfg := ClientCfg
	ClientCfg = config.DefaultClientConfig()
	defer func() { ClientCfg = oldCfg }()

	mk := func(n int) string {
		var b strings.Builder
		for i := 1; i <= n; i++ {
			b.WriteString("lorem ipsum dolor sit amet\n")
		}
		return b.String()
	}
	wire := WireMessage{ChainHeight: 99}

	// System tab never folds.
	if out := maybeCollapse(mk(30), wire, TabSystem); strings.Contains(out, "/expand") {
		t.Fatal("system tab must not fold")
	}
	// Short heads pass through.
	if out := maybeCollapse(mk(3), wire, TabChat); strings.Contains(out, "/expand") {
		t.Fatal("short head must not fold")
	}
	// Long chat head folds with height address.
	if out := maybeCollapse(mk(30), wire, TabChat); !strings.Contains(out, "/expand #99") {
		t.Fatalf("long head must fold with address, got %q", out[len(out)-40:])
	}
	// Disabled config passes through.
	ClientCfg.UI.Collapse.Enabled = &[]bool{false}[0]
	if out := maybeCollapse(mk(30), wire, TabChat); strings.Contains(out, "/expand") {
		t.Fatal("disabled collapse must pass through")
	}
}

func TestFindCollapsed(t *testing.T) {
	lines := []string{
		"| 12:00 Alice: hello\n",
		"| line one\n| line two\x1b[90m...\x1b[0m [Xem thêm: /expand #42]\n",
		"| 12:01 Bob: short\n",
	}
	if got := findCollapsed(lines, 42); got != 1 {
		t.Fatalf("find = %d, want 1", got)
	}
	if got := findCollapsed(lines, 4); got != -1 {
		t.Fatalf("#4 must not match #42's trailer, got %d", got)
	}
	if got := findCollapsed(lines, 43); got != -1 {
		t.Fatalf("missing height = %d, want -1", got)
	}
	if got := findCollapsed(nil, 42); got != -1 {
		t.Fatal("nil buffer must return -1")
	}
}

func TestExpandTrailer(t *testing.T) {
	if got := expandTrailer(1234); got != "\x1b[90m...\x1b[0m [Xem thêm: /expand #1234]" {
		t.Fatalf("trailer = %q", got)
	}
}

func TestCollapseAccessors(t *testing.T) {
	c := config.DefaultClientConfig()
	if !c.CollapseEnabled() {
		t.Fatal("default must enable collapse")
	}
	if c.CollapseRows() != 10 || c.CollapsePreviewRows() != 5 {
		t.Fatalf("defaults = %d/%d, want 10/5", c.CollapseRows(), c.CollapsePreviewRows())
	}
	c.UI.Collapse.Rows = 1
	if c.CollapseRows() != 10 {
		t.Fatal("rows < 3 must clamp to default")
	}
	c.UI.Collapse.Rows = 10
	c.UI.Collapse.PreviewRows = 50
	if c.CollapsePreviewRows() != 9 {
		t.Fatalf("preview >= rows must clamp to rows-1, got %d", c.CollapsePreviewRows())
	}
	var nilCfg *config.ClientConfig
	if !nilCfg.CollapseEnabled() || nilCfg.CollapseRows() != 10 || nilCfg.CollapsePreviewRows() != 5 {
		t.Fatal("nil config must fall back to defaults")
	}
}

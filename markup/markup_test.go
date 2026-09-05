package markup

import (
	"strings"
	"testing"

	"github.com/CleveTok3125/V2V/codebg"
)

func TestTrio(t *testing.T) {
	cases := []struct{ in, want string }{
		{"**bold**", "\x1b[1mbold\x1b[22m"},
		{"*italic*", "\x1b[3mitalic\x1b[23m"},
		{"~~strike~~", "\x1b[9mstrike\x1b[29m"},
		{"__bold__", "\x1b[1mbold\x1b[22m"},
		{"_italic_", "\x1b[3mitalic\x1b[23m"},
		{"a **b** c", "a \x1b[1mb\x1b[22m c"},
		{"**bold *nested* end**", "\x1b[1mbold \x1b[3mnested\x1b[23m end\x1b[22m"},
		// Unmatched markers and intra-word runs stay literal.
		{"unmatched **bold", "unmatched **bold"},
		{"a*b", "a*b"},
		{"file_name", "file_name"},
		// Goldmark strikes single-tilde pairs too; unmatched stays literal.
		{"~single~", "\x1b[9msingle\x1b[29m"},
		{"a~b", "a~b"},
	}
	for _, c := range cases {
		if got := Span(c.in, codebg.DefaultStyle()); got != c.want {
			t.Errorf("Span(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestLink(t *testing.T) {
	linked := "\x1b]8;;https://example.com/a\x1b\\\x1b[94;4mdocs\x1b[0m\x1b]8;;\x1b\\"
	cases := []struct{ in, want string }{
		{"see [docs](https://example.com/a) now", "see " + linked + " now"},
		// Non-http destinations, titles and empty targets stay literal.
		{"see [x](javascript:alert(1)) now", "see [x](javascript:alert(1)) now"},
		{`see [t](https://e.com "title") now`, `see [t](https://e.com "title") now`},
		{"see (/path) now", "see (/path) now"},
		// Bare URLs are linkify's job, not markup's.
		{"bare https://example.com/x here", "bare https://example.com/x here"},
		// Autolinks render; email keeps its brackets.
		{"url <https://example.com/z> ok",
			"url \x1b]8;;https://example.com/z\x1b\\\x1b[94;4mhttps://example.com/z\x1b[0m\x1b]8;;\x1b\\ ok"},
		{"mail <a@b.c> ok", "mail <a@b.c> ok"},
		// Images stay literal (chat renders no images).
		{"![alt](https://example.com/i.png)", "![alt](https://example.com/i.png)"},
	}
	for _, c := range cases {
		if got := Span(c.in, codebg.DefaultStyle()); got != c.want {
			t.Errorf("Span(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestQuote(t *testing.T) {
	bar := "\x1b[90m│ "
	off := "\x1b[39m"
	cases := []struct{ in, want string }{
		{"> quoted", bar + "quoted" + off},
		{"> a\n> b", bar + "a" + off + "\n" + bar + "b" + off},
		{"> a\n\n> b", bar + "a" + off + "\n\n" + bar + "b" + off},
		{">", ">"},
		{" > spaced", bar + "spaced" + off},
		{">> nested", bar + bar + "nested" + off + off},
		// Not line-leading: literal.
		{"a > b", "a > b"},
		// Markers that are not quotes stay literal.
		{"# heading", "# heading"},
		{"- a\n- b", "- a\n- b"},
		{"a\n===", "a\n==="},
		{"***", "***"},
		{"<div>html</div>", "<div>html</div>"},
	}
	for _, c := range cases {
		if got := Span(c.in, codebg.DefaultStyle()); got != c.want {
			t.Errorf("Span(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestCodeDelegation pins single-source code styling: pure-code inputs
// are byte-identical to codebg on both paths.
func TestCodeDelegation(t *testing.T) {
	st := codebg.DefaultStyle()
	cases := []string{
		"`code`",
		"```go\nfmt.Println(1)\n```",
		"```\nplain\n```",
		"```unclosed\nline",
	}
	for _, in := range cases {
		if got, want := Span(in, st), codebg.RenderWithStyle(in, st); got != want {
			t.Errorf("Span(%q) = %q, want codebg %q", in, got, want)
		}
		if got, want := SpanPlain(in), codebg.Render(in); got != want {
			t.Errorf("SpanPlain(%q) = %q, want codebg %q", in, got, want)
		}
	}
}

// TestMixedMarkup pins intended interactions: trio wraps code spans, and
// multiline code spans keep terminal rows (joined by newline, not
// CommonMark spaces) so erase math holds.
func TestMixedMarkup(t *testing.T) {
	st := codebg.DefaultStyle()
	cases := []struct{ in, want string }{
		{"**hi `code` bye**", "\x1b[1mhi \x1b[48;5;236mcode\x1b[49m bye\x1b[22m"},
		{"`a\nb`", "\x1b[48;5;236ma\nb\x1b[49m"},
	}
	for _, c := range cases {
		if got := Span(c.in, st); got != c.want {
			t.Errorf("Span(%q) = %q, want %q", c.in, got, c.want)
		}
		if got := SpanPlain(c.in); got != c.want {
			t.Errorf("SpanPlain(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestRowPreservation pins the erase-math invariant: styling never adds
// or removes newlines, except fenced blocks which reshape exactly like
// codebg (header added, closer dropped) on both render paths.
func TestRowPreservation(t *testing.T) {
	st := codebg.DefaultStyle()
	inputs := []string{
		"plain",
		"hello **bold** and *it* and ~~st~~",
		"a\n\nb",
		"trail\n",
		"a  \nb",
		"> q1\n> q2\n\n> q3",
		"x [t](https://e.com/y) z",
		"**multi\nline** emph",
		"# h\n- l\npara",
		"",
		"\x1b[31mred\x1b[0m",
	}
	for _, in := range inputs {
		for name, got := range map[string]string{
			"Span":      Span(in, st),
			"SpanPlain": SpanPlain(in),
		} {
			if a, b := len(strings.Split(in, "\n")), len(strings.Split(got, "\n")); a != b {
				t.Errorf("%s(%q) rows %d->%d", name, in, a, b)
			}
		}
	}
}

// TestPathsAgree requires the styled and plain paths to share newline
// structure across a corpus, so placeholder and echo erase identically.
func TestPathsAgree(t *testing.T) {
	st := codebg.DefaultStyle()
	inputs := []string{
		"plain",
		"**b** and `c` and [t](https://e.com) and > q",
		"> quote\nwith **bold**",
		"```go\nx := 1\n```",
		"```\nplain\n```",
		"a\n\nb\n\nc",
		"unmatched **x and `y",
		"# h\n- l1\n- l2\n\npara *e*",
	}
	for _, in := range inputs {
		a := len(strings.Split(Span(in, st), "\n"))
		b := len(strings.Split(SpanPlain(in), "\n"))
		if a != b {
			t.Errorf("paths disagree on %q: styled %d vs plain %d", in, a, b)
		}
	}
}

func TestANSIPassthrough(t *testing.T) {
	in := "esc \x1b[31mred\x1b[0m **bold**"
	if got := Span(in, codebg.DefaultStyle()); got != in {
		t.Errorf("ESC input must pass through, got %q", got)
	}
	if got := SpanPlain(in); got != in {
		t.Errorf("ESC input must pass through, got %q", got)
	}
}

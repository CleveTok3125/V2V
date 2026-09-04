package codebg

import (
	"strings"
	"testing"
)

func TestRenderPlainUnchanged(t *testing.T) {
	for _, s := range []string{"", "hello world", "no backticks here", "100% sure"} {
		if got := Render(s); got != s {
			t.Errorf("Render(%q) = %q, want unchanged", s, got)
		}
	}
}

func TestRenderInline(t *testing.T) {
	got := Render("run `go vet` now")
	want := "run \x1b[48;5;236m`go vet`\x1b[49m now"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestRenderInlineUnmatchedLeftLiteral(t *testing.T) {
	for _, s := range []string{"a `b", "just ` one", "``", "a `` b"} {
		if got := Render(s); got != s {
			t.Errorf("Render(%q) = %q, want unchanged", s, got)
		}
	}
}

func TestRenderInlineMultiple(t *testing.T) {
	got := Render("`a` and `b`")
	if strings.Count(got, "\x1b[48;5;236m") != 2 || strings.Count(got, "\x1b[49m") != 2 {
		t.Errorf("expected two spans, got %q", got)
	}
	if !strings.Contains(got, "`a`") || !strings.Contains(got, "`b`") {
		t.Errorf("backticks must stay visible, got %q", got)
	}
}

func TestRenderFencedBlock(t *testing.T) {
	in := "```\nhello\nworld\n```"
	got := Render(in)
	lines := strings.Split(got, "\n")
	if lines[0] != "```" || lines[3] != "```" {
		t.Errorf("fence markers must pass through, got %q", got)
	}
	if lines[1] != "\x1b[48;5;236mhello\x1b[49m" || lines[2] != "\x1b[48;5;236mworld\x1b[49m" {
		t.Errorf("block content must get background, got %q", got)
	}
}

func TestRenderFencedWithLang(t *testing.T) {
	in := "```go\nfmt.Println()\n```"
	got := Render(in)
	if !strings.Contains(got, "\x1b[48;5;236mfmt.Println()\x1b[49m") {
		t.Errorf("fenced content with lang tag must get background, got %q", got)
	}
}

func TestRenderSingleLineFence(t *testing.T) {
	got := Render("```code```")
	if got != "\x1b[48;5;236m```code```\x1b[49m" {
		t.Errorf("single-line fence must get background, got %q", got)
	}
}

func TestRenderBackticksInsideFenceLiteral(t *testing.T) {
	in := "```\n`not inline`\n```"
	lines := strings.Split(Render(in), "\n")
	if lines[1] != "\x1b[48;5;236m`not inline`\x1b[49m" {
		t.Errorf("backticks inside fence are literal block content, got %q", lines[1])
	}
}

func TestRenderEscUnchanged(t *testing.T) {
	s := "\x1b[90m`hi`\x1b[0m"
	if got := Render(s); got != s {
		t.Errorf("text with ESC must pass through, got %q", got)
	}
}

func TestRenderKeepsVisibleChars(t *testing.T) {
	strip := func(s string) string {
		s = strings.ReplaceAll(s, "\x1b[48;5;236m", "")
		return strings.ReplaceAll(s, "\x1b[49m", "")
	}
	for _, s := range []string{"run `go vet` now", "```\nhi\n```", "`a` and `b`"} {
		if strip(Render(s)) != s {
			t.Errorf("visible chars changed for %q", s)
		}
	}
}

package codebg

import (
	"strings"
	"testing"
)

func TestNeedsContinuation(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"```", true},
		{"```go", true},
		{"   ```", true},
		{"```code```", false},
		{"```go fmt.Println() ```", false},
		{"``````", false},
		{"hello", false},
		{"`code`", false},
		{"", false},
	}
	for _, c := range cases {
		if got := NeedsContinuation(c.in); got != c.want {
			t.Errorf("NeedsContinuation(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestRenderPlainUnchanged(t *testing.T) {
	for _, s := range []string{"", "hello world", "no backticks here", "100% sure"} {
		if got := Render(s); got != s {
			t.Errorf("Render(%q) = %q, want unchanged", s, got)
		}
	}
}

func TestRenderInlineStripsDelimiters(t *testing.T) {
	got := Render("run `go vet` now")
	want := "run \x1b[48;5;236mgo vet\x1b[49m now"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if strings.Contains(got, "`") {
		t.Errorf("delimiters must be stripped, got %q", got)
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
	if strings.Contains(got, "`") {
		t.Errorf("delimiters must be stripped, got %q", got)
	}
}

func TestRenderFencedBlockNoMarkers(t *testing.T) {
	in := "```\nhello\nworld\n```"
	got := Render(in)
	lines := strings.Split(got, "\n")
	if len(lines) != 2 {
		t.Fatalf("markers must be hidden, want 2 lines, got %q", got)
	}
	if lines[0] != "\x1b[48;5;236mhello\x1b[49m" || lines[1] != "\x1b[48;5;236mworld\x1b[49m" {
		t.Errorf("block content must get background, got %q", got)
	}
}

func TestRenderFencedWithLangHeader(t *testing.T) {
	in := "```go\nfmt.Println()\n```"
	got := Render(in)
	lines := strings.Split(got, "\n")
	if len(lines) != 2 {
		t.Fatalf("want header + 1 content line, got %q", got)
	}
	if lines[0] != "\x1b[48;5;236mgo\x1b[49m" {
		t.Errorf("first line must be the language header, got %q", lines[0])
	}
	if lines[1] != "\x1b[48;5;236mfmt.Println()\x1b[49m" {
		t.Errorf("content must get background, got %q", lines[1])
	}
}

func TestRenderUnclosedFence(t *testing.T) {
	in := "```go\nfmt.Println()"
	got := Render(in)
	lines := strings.Split(got, "\n")
	if len(lines) != 2 || lines[0] != "\x1b[48;5;236mgo\x1b[49m" {
		t.Errorf("unclosed fence keeps header + bg to end, got %q", got)
	}
}

func TestRenderSingleLineFence(t *testing.T) {
	got := Render("```code```")
	if got != "\x1b[48;5;236mcode\x1b[49m" {
		t.Errorf("single-line fence must strip markers, got %q", got)
	}
}

func TestRenderSingleLineFenceWithLang(t *testing.T) {
	got := Render("```go fmt.Println()```")
	lines := strings.Split(got, "\n")
	if len(lines) != 2 {
		t.Fatalf("want header + content, got %q", got)
	}
	if lines[0] != "\x1b[48;5;236mgo\x1b[49m" || lines[1] != "\x1b[48;5;236mfmt.Println()\x1b[49m" {
		t.Errorf("got %q", got)
	}
}

func TestRenderBackticksInsideFenceAreContent(t *testing.T) {
	in := "```\n`not inline`\n```"
	lines := strings.Split(Render(in), "\n")
	if len(lines) != 1 || lines[0] != "\x1b[48;5;236m`not inline`\x1b[49m" {
		t.Errorf("backticks inside fence stay literal block content, got %q", lines)
	}
}

func TestRenderEscUnchanged(t *testing.T) {
	s := "\x1b[90m`hi`\x1b[0m"
	if got := Render(s); got != s {
		t.Errorf("text with ESC must pass through, got %q", got)
	}
}

func TestRenderIndentedFence(t *testing.T) {
	in := "   ```\nhi\n   ```"
	got := Render(in)
	if got != "\x1b[48;5;236mhi\x1b[49m" {
		t.Errorf("indented fences must toggle, got %q", got)
	}
}

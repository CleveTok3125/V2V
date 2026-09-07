package filter

import (
	"strings"
	"testing"
)

func TestValidateMessage(t *testing.T) {
	ok := []string{"hello", "a trung 😀", "https://example.com", "line1\nline2"}
	for _, s := range ok {
		if err := ValidateMessage(s); err != nil {
			t.Errorf("should pass %q: %v", s, err)
		}
	}
	bad := []string{"\x1b[31mred", "a\u200b", "e\u0301", "\u2028", "\x00", "\uFFFD", "\xff"}
	for _, s := range bad {
		if err := ValidateMessage(s); err == nil {
			t.Errorf("should reject %q", s)
		}
	}
}

func TestValidateDisplayName(t *testing.T) {
	if err := ValidateDisplayName("Alice"); err != nil {
		t.Error(err)
	}
	if err := ValidateDisplayName("a\nb"); err == nil {
		t.Error("should reject newline")
	}
}

func TestSanitizeForDisplay(t *testing.T) {
	in := "\x1b[90m12:34\x1b[0m hello \x1b[2J"
	out := SanitizeForDisplay(in)
	if out != "\x1b[90m12:34\x1b[0m hello " {
		t.Errorf("unexpected %q", out)
	}
}

// A legit OSC8 trip-badge link carrying SGR-colored text must survive
// intact.
func TestSanitize_OSC8Hyperlink(t *testing.T) {
	in := "\x1b]8;;https://chat.example.com/verify?pub=ab\x1b\\◆ \x1b[38;2;79;129;255mdeadbeef\x1b[0m\x1b]8;;\x1b\\"
	out := SanitizeForDisplay(in)
	if out != in {
		t.Errorf("OSC8+SGR mangled:\n got %q\nwant %q", out, in)
	}
	// Unterminated OSC8 must not leak the raw URL.
	bad := "\x1b]8;;https://evil.example.com"
	if got := SanitizeForDisplay(bad); strings.Contains(got, "evil") {
		t.Errorf("unterminated OSC8 leaked: %q", got)
	}
}

package filter

import "testing"

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

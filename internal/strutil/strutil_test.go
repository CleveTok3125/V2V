package strutil

import "testing"

func TestShortN(t *testing.T) {
	if got := ShortN("abcdef", 2); got != "ab…" {
		t.Errorf("ShortN long = %q", got)
	}
	if got := ShortN("ab", 12); got != "ab" {
		t.Errorf("ShortN short must pass through, got %q", got)
	}
	if got := ShortN("", 8); got != "" {
		t.Errorf("ShortN empty = %q", got)
	}
	if got := ShortN("abc", 0); got != "abc" {
		t.Errorf("ShortN zero width = %q", got)
	}
	if got := Short("abcdefghijklmnopqrstuvwxyz"); got != "abcdefghijkl…" {
		t.Errorf("Short default width = %q", got)
	}
}

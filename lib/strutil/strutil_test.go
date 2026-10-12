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

func TestShortNMultibyte(t *testing.T) {
	// Truncation counts runes, never splits a UTF-8 sequence: the first
	// 4 runes of "日本語test" plus an ellipsis.
	if got := ShortN("日本語test", 4); got != "日本語t…" {
		t.Errorf("ShortN multibyte = %q", got)
	}
	if got := Short("日本語テストですよ長い文です"); got != "日本語テストですよ長い文…" {
		t.Errorf("Short multibyte = %q", got)
	}
}

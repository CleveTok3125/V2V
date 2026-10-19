package main

import (
	"strings"
	"testing"
)

func TestEntryChromeStyles(t *testing.T) {
	if got := dimStyle.Render("x"); got != "\x1b[90mx\x1b[m" {
		t.Fatalf("dim = %q", got)
	}
	if got := warnStyle.Render("✗"); got != "\x1b[91m✗\x1b[m" {
		t.Fatalf("warn = %q", got)
	}
	if got := upvoteStyle.Render("▲1"); got != "\x1b[92m▲1\x1b[m" {
		t.Fatalf("upvote = %q", got)
	}
	badge := badgeStyle("abcd1234").Render("◆ abcd1234")
	if !strings.HasPrefix(badge, "\x1b[38;2;") || !strings.HasSuffix(badge, "\x1b[m") {
		t.Fatalf("badge = %q", badge)
	}
	if got := linkText("https://example.com/x", "y"); got != "\x1b]8;;https://example.com/x\x1b\\y\x1b]8;;\x1b\\" {
		t.Fatalf("link = %q", got)
	}
}

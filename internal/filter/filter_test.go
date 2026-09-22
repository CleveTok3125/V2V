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

// Non-OSC8 OSC sequences (clipboard write, palette, colors, kitty) must
// never reach the terminal, even when properly ST-terminated.
func TestSanitize_OSCNon8Blocked(t *testing.T) {
	cases := []string{
		"\x1b]52;c;aGVsbG8=\x1b\\",
		"\x1b]4;1;rgb:ff/00/00\x1b\\",
		"\x1b]10;#ffffff\x1b\\",
		"\x1b]11;#000000\x1b\\",
		"\x1b]104\x1b\\",
		"\x1b]1337;File=name=YWJj\x1b\\",
	}
	for _, in := range cases {
		if got := SanitizeForDisplay(in); got != "" {
			t.Errorf("OSC not stripped: in=%q got=%q", in, got)
		}
		// Surrounding text survives; only the OSC payload vanishes.
		wrapped := "a" + in + "b"
		if got := SanitizeForDisplay(wrapped); got != "ab" {
			t.Errorf("surrounding text mangled: in=%q got=%q", in, got)
		}
	}
}

// OSC8 hyperlinks may only target http(s) (or the client-generated
// v2v://expand/ in-app command). Dangerous schemes are dropped, the
// visible label stays.
func TestSanitize_OSC8SchemeBlocked(t *testing.T) {
	for _, uri := range []string{
		"javascript:alert(1)",
		"file:///etc/passwd",
		"data:text/html,<b>x</b>",
		"vscode://x",
		"ssh://host",
		"\x1b]52;c;AAAA", // nested escape smuggled as target
	} {
		in := "\x1b]8;;" + uri + "\x1b\\label\x1b]8;;\x1b\\"
		got := SanitizeForDisplay(in)
		if strings.Contains(got, uri) {
			t.Errorf("bad OSC8 target survived: uri=%q got=%q", uri, got)
		}
		if !strings.Contains(got, "label") {
			t.Errorf("visible label lost: uri=%q got=%q", uri, got)
		}
	}
}

// http and https targets, plus the in-app expand link, stay byte-exact.
func TestSanitize_OSC8AllowedSchemes(t *testing.T) {
	for _, uri := range []string{
		"http://example.com",
		"https://example.com/a?b=c",
		"HTTPS://example.com",
		"v2v://expand/42",
	} {
		in := "\x1b]8;;" + uri + "\x1b\\label\x1b]8;;\x1b\\"
		if got := SanitizeForDisplay(in); got != in {
			t.Errorf("allowed OSC8 mangled: uri=%q\n got %q\nwant %q", uri, got, in)
		}
	}
}

// An allowed scheme must not let control bytes ride inside the emitted
// sequence: a nested ESC or BEL would end the OSC early and the tail
// would run as a fresh escape sequence. The whole sequence is dropped.
func TestSanitize_OSC8EmbeddedControlBlocked(t *testing.T) {
	cases := []string{
		"\x1b]8;;http://a\x1b[2J\x1b\\label\x1b]8;;\x1b\\",
		"\x1b]8;x\x1b[2Jy;http://a\x1b\\label\x1b]8;;\x1b\\",
		"\x1b]8;;https://x\x1b]52;c;DATA\x1b\\",
		"\x1b]8;;https://x\x07y\x1b\\label\x1b]8;;\x1b\\",
		"\x1b]8;;https://x\ny\x1b\\label\x1b]8;;\x1b\\",
	}
	for _, in := range cases {
		got := SanitizeForDisplay(in)
		// Re-scanning the result must not yield a fresh ESC or a
		// smuggled clipboard payload: the only escapes left are SGR and
		// the two closing OSC8 terminators of the label link.
		re := SanitizeForDisplay(got)
		if re != got {
			t.Errorf("sanitized output is not a fixed point: in=%q got=%q re=%q", in, got, re)
		}
	}
}

// An OSC terminated by BEL (0x07) instead of ST must be treated as
// unterminated: fail closed, drop the tail, never leak the payload.
func TestSanitize_OSCBELTerminatorDropsTail(t *testing.T) {
	got := SanitizeForDisplay("\x1b]52;c;U0VDUkVU\x07after")
	if strings.Contains(got, "U0VDUkVU") || strings.Contains(got, "after") {
		t.Errorf("BEL-terminated OSC leaked: %q", got)
	}
}

// SanitizeSingleLine is SanitizeForDisplay with newlines removed, for
// one-line fields (time, display name, host).
func TestSanitizeSingleLine(t *testing.T) {
	in := "\x1b[31mAlice\x1b[0m\nBob\r\x1b]8;;https://example.com\x1b\\href"
	want := "\x1b[31mAlice\x1b[0mBob\x1b]8;;https://example.com\x1b\\href"
	if got := SanitizeSingleLine(in); got != want {
		t.Errorf("got %q want %q", got, want)
	}
}

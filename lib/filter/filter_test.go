package filter

import (
	"errors"
	"strings"
	"testing"
	"unicode"
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
	allowed := []struct {
		label string
		name  string
	}{
		{"latin", "Alice"},
		{"latin with space", "John Smith"},
		{"latin separators", "a_b-c.d'e"},
		{"latin with digits", "user1234"},
		{"vietnamese nfc", "Nguyễn Văn A"},
		{"cyrillic", "Иван"},
		{"greek", "Δημήτρης"},
		{"cjk", "张三"},
		{"cjk numerals as letters", "一二三"},
		{"hangul", "김철수"},
		{"arabic", "محمد"},
		{"hebrew", "דוד"},
		{"thai", "สมชาย"},
		{"devanagari spacing marks", "राम"},
		{"non-ascii decimal digits", "user١٢٣"},
	}
	for _, tc := range allowed {
		if err := ValidateDisplayName(tc.name); err != nil {
			t.Errorf("%s: %q should be allowed, got %v", tc.label, tc.name, err)
		}
	}

	blocked := []struct {
		label string
		name  string
	}{
		{"empty", ""},
		{"spaces only", "   "},
		{"underscores only", "___"},
		{"dots only", "..."},
		{"lone combining mark", "\u0301"},
		{"latin with combining mark", "a\u0301"},
		{"vietnamese nfd", "Nguye\u0302\u0303n Va\u0306n A"},
		{"thai tone mark", "สม\u0e48ชาย"},
		{"newline", "a\nb"},
		{"tab", "a\tb"},
		{"esc", "a\x1bb"},
		{"replacement char", "a\ufffdb"},
		{"rtl override", "a\u202eb"},
		{"zero width joiner", "a\u200db"},
		{"gutter", "a|b"},
		{"bullet separator", "a\u2022b"},
		{"fake message number", "x | #99:ffff"},
		{"hash", "a#1"},
		{"colon", "a:b"},
		{"at", "a@b"},
		{"asterisk", "a*b"},
		{"backtick", "a`b"},
		{"brackets", "a[b]"},
		{"angle brackets", "a<b>"},
		{"slash", "a/b"},
		{"backslash", "a\\b"},
		{"pencil", "a\u270db"},
		{"diamond", "a\u25c6b"},
		{"reply arrow", "a\u21a9b"},
		{"box drawing", "a\u250cb"},
		{"middot", "a\u00b7b"},
		{"emoji", "a\U0001F600b"},
		{"fullwidth hash", "a\uff03b"},
		{"vulgar fraction", "a\u00bdb"},
	}
	for _, tc := range blocked {
		if err := ValidateDisplayName(tc.name); err == nil {
			t.Errorf("%s: %q should be rejected", tc.label, tc.name)
		}
	}
}

// TestDisplayNameRuleIsSubsetOfLegacy is the guarantee that the allowlist only
// tightens: over every code point, a rune the new rule accepts is a rune
// displayLegacyAllowed accepted too. A rune accepted here but refused there
// would mean the change widened the policy instead of narrowing it.
func TestDisplayNameRuleIsSubsetOfLegacy(t *testing.T) {
	widened := 0
	for r := rune(0); r <= unicode.MaxRune; r++ {
		if r >= 0xD800 && r <= 0xDFFF {
			continue // lone surrogates are not encodable text
		}
		if ValidateDisplayName("a"+string(r)+"a") != nil {
			continue
		}
		if !displayLegacyAllowed(r) {
			if widened < 5 {
				t.Errorf("U+%04X %q is accepted now but the legacy rule refused it", r, r)
			}
			widened++
		}
	}
	if widened > 5 {
		t.Errorf("%d code points widened in total", widened)
	}
	if displayLegacyAllowed('|') && ValidateDisplayName("a|a") == nil {
		t.Error("the rule is not a strict tightening: a|a is still accepted")
	}
}

// An admin prefix keeps the permissive legacy rule so an existing roles.json
// keeps working; only the runes that build a header are refused.
func TestValidateDisplayPrefix(t *testing.T) {
	allowed := []string{"", "[Admin] ", "*", "@", "«Admin»", "★Admin★", "Moderator "}
	for _, prefix := range allowed {
		if err := ValidateDisplayPrefix(prefix); err != nil {
			t.Errorf("%q should be allowed, got %v", prefix, err)
		}
	}
	blocked := []string{"| ", "• ", "#99:ffff", "a:b", "✍ ", "◆ ", "↩ ", "┌─ ", "a·b", "a\nb", "a\x1bb"}
	for _, prefix := range blocked {
		if err := ValidateDisplayPrefix(prefix); err == nil {
			t.Errorf("%q should be rejected", prefix)
		}
	}
	if err := ValidateDisplayPrefix("a|b"); !errors.Is(err, ErrInvalidDisplayPrefix) {
		t.Fatalf("want ErrInvalidDisplayPrefix, got %v", err)
	}
}

func TestNeutralizeDisplayName(t *testing.T) {
	cases := []struct {
		label string
		in    string
		want  string
	}{
		{"clean name", "Alice", "Alice"},
		{"keeps the hash tail", "Alice#a1b2", "Alice#a1b2"},
		{"keeps a serial", "Alice#a1b2-2", "Alice#a1b2-2"},
		{"keeps an admin prefix", "[Admin] Alice#a1b2", "[Admin] Alice#a1b2"},
		{"forges a gutter", "x | y#a1b2", "x ? y#a1b2"},
		{"forges a reference", "x | #99:ffff#a1b2", "x ? ?99?ffff#a1b2"},
		{"forges a badge", "x ✍ ◆ deadbeef#a1b2", "x ? ? deadbeef#a1b2"},
		{"forges a quote", "┌─ ↩ #12#a1b2", "?? ? ?12#a1b2"},
		{"hash that is not a tail", "a#b#a1b2", "a?b#a1b2"},
		{"no hash at all", "a|b", "a?b"},
	}
	for _, tc := range cases {
		if got := NeutralizeDisplayName(tc.in); got != tc.want {
			t.Errorf("%s: %q -> %q, want %q", tc.label, tc.in, got, tc.want)
		}
	}
}

// Every rejection carries the sentinel, so a caller can classify without
// matching the message text.
func TestValidateDisplayNameErrorIsSentinel(t *testing.T) {
	err := ValidateDisplayName("a|b")
	if !errors.Is(err, ErrInvalidDisplayName) {
		t.Fatalf("want ErrInvalidDisplayName, got %v", err)
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

// OSC8 hyperlinks may only target http(s). Dangerous schemes are dropped,
// the visible label stays.
func TestSanitize_OSC8SchemeBlocked(t *testing.T) {
	for _, uri := range []string{
		"javascript:alert(1)",
		"file:///etc/passwd",
		"data:text/html,<b>x</b>",
		"vscode://x",
		"ssh://host",
		"v2v://expand/42",
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

// http and https targets stay byte-exact.
func TestSanitize_OSC8AllowedSchemes(t *testing.T) {
	for _, uri := range []string{
		"http://example.com",
		"https://example.com/a?b=c",
		"HTTPS://example.com",
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

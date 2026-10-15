package filter

import (
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

var (
	ErrInvalidCharacter     = errors.New("message contains invalid character")
	ErrInvalidDisplayName   = errors.New("display name contains invalid character")
	ErrInvalidDisplayPrefix = errors.New("display prefix contains invalid character")
)

// ValidateMessage checks chat message before send/broadcast.
// Allows '\n' and IsGraphic, forbids 0xFFFD, Cf, Mn, Me, Zl, Zp, non-graphic.
func ValidateMessage(text string) error {
	if !utf8.ValidString(text) {
		return fmt.Errorf("%w: invalid utf8", ErrInvalidCharacter)
	}
	for _, r := range text {
		if r == '\n' {
			continue
		}
		if r == 0xFFFD {
			return fmt.Errorf("%w: U+FFFD", ErrInvalidCharacter)
		}
		if unicode.Is(unicode.Cf, r) {
			return fmt.Errorf("%w: Cf U+%04X", ErrInvalidCharacter, r)
		}
		if unicode.Is(unicode.Mn, r) || unicode.Is(unicode.Me, r) {
			return fmt.Errorf("%w: Mn/Me U+%04X", ErrInvalidCharacter, r)
		}
		if unicode.Is(unicode.Zl, r) || unicode.Is(unicode.Zp, r) {
			return fmt.Errorf("%w: Zl/Zp U+%04X", ErrInvalidCharacter, r)
		}
		if !unicode.IsGraphic(r) {
			return fmt.Errorf("%w: non-graphic U+%04X", ErrInvalidCharacter, r)
		}
	}
	return nil
}

// displayNameSeparators are the only non-letter, non-digit runes a display
// name may carry: a space between words plus the common name punctuation.
const displayNameSeparators = " _-.'"

// displayStructuralRunes build a chat header, the meta strip beside it, or a
// badge or quote glyph: "| HH:MM name • #height:hash4 ✍️ badge" and the quote
// row "|   ┌─  ↩ #height: name: text". A name that carries one of them shows a
// reader structure the server never sent.
const displayStructuralRunes = "|•#:✍◆↩┌└─·"

// displayLegacyAllowed is the rule a display name followed before it became an
// allowlist: any graphic rune but newline, U+FFFD, and the Cf, Mn, Me, Zl and
// Zp categories. An admin-authored prefix keeps this permissive rule so an
// existing roles.json keeps working, and loses only displayStructuralRunes.
func displayLegacyAllowed(r rune) bool {
	switch {
	case r == '\n', r == 0xFFFD:
		return false
	case unicode.Is(unicode.Cf, r):
		return false
	case unicode.Is(unicode.Mn, r) || unicode.Is(unicode.Me, r):
		return false
	case unicode.Is(unicode.Zl, r) || unicode.Is(unicode.Zp, r):
		return false
	case !unicode.IsGraphic(r) && r != ' ':
		return false
	}
	return true
}

// ValidateDisplayName accepts only letters, decimal digits, spacing combining
// marks and displayNameSeparators, and requires at least one letter or digit.
//
// An allowlist is used because a denylist cannot name the characters that make
// a name read as structure: "|", "•", "#", ":" and the client's own glyphs are
// ordinary punctuation to a denylist, yet each forges a part of the header row.
//
// The rule only ever refuses more than the one it replaced, never less: it is
// a subset of displayLegacyAllowed, which
// TestDisplayNameRuleIsSubsetOfLegacy proves over every code point. Mn and Me
// stay refused for that reason, so Thai tone marks, Arabic and Hebrew vowel
// points and decomposed Vietnamese are still rejected — the legacy rule
// already rejected them, and widening that is a separate decision.
func ValidateDisplayName(name string) error {
	if !utf8.ValidString(name) {
		return fmt.Errorf("%w: invalid utf8", ErrInvalidDisplayName)
	}
	hasLetterOrDigit := false
	for _, r := range name {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			hasLetterOrDigit = true
			continue
		}
		if unicode.Is(unicode.Mc, r) {
			continue
		}
		if strings.ContainsRune(displayNameSeparators, r) {
			continue
		}
		return fmt.Errorf("%w: U+%04X %q", ErrInvalidDisplayName, r, r)
	}
	if !hasLetterOrDigit {
		return fmt.Errorf("%w: no letter or digit", ErrInvalidDisplayName)
	}
	return nil
}

// ValidateDisplayPrefix checks an admin-authored CustomPrefix, the "[Admin] "
// in "[Admin] name#hash". It is config, not user input, so it keeps
// displayLegacyAllowed instead of the name allowlist: existing prefixes use
// brackets or "*", and neither spoofs a header. What it may not carry is a
// rune that builds one. An empty prefix is valid.
func ValidateDisplayPrefix(prefix string) error {
	if !utf8.ValidString(prefix) {
		return fmt.Errorf("%w: invalid utf8", ErrInvalidDisplayPrefix)
	}
	for _, r := range prefix {
		if strings.ContainsRune(displayStructuralRunes, r) || !displayLegacyAllowed(r) {
			return fmt.Errorf("%w: U+%04X %q", ErrInvalidDisplayPrefix, r, r)
		}
	}
	return nil
}

// NeutralizeDisplayName makes a stored display name safe to print in a header
// row. A name minted under the current rule needs nothing; one already in
// history may still carry displayStructuralRunes, so each becomes "?". The
// trailing "#hash", with its optional "-serial", is kept, because every
// display name ends with it and it is the one "#" a header expects.
func NeutralizeDisplayName(name string) string {
	body, tail := splitDisplayHashSuffix(name)
	if !strings.ContainsAny(body, displayStructuralRunes) {
		return name
	}
	var sb strings.Builder
	sb.Grow(len(name))
	for _, r := range body {
		if strings.ContainsRune(displayStructuralRunes, r) {
			sb.WriteRune('?')
			continue
		}
		sb.WriteRune(r)
	}
	sb.WriteString(tail)
	return sb.String()
}

// splitDisplayHashSuffix splits the "#hash" tail generateDisplayName always
// appends, so a neutralizer can leave it alone. A name that does not end in
// one is body throughout.
func splitDisplayHashSuffix(name string) (body, tail string) {
	hash := strings.LastIndexByte(name, '#')
	if hash < 0 {
		return name, ""
	}
	digits := name[hash+1:]
	if serial := strings.LastIndexByte(digits, '-'); serial >= 0 {
		if !isDecimalDigits(digits[serial+1:]) {
			return name, ""
		}
		digits = digits[:serial]
	}
	if len(digits) < 4 || len(digits) > 6 || !isHexDigits(digits) {
		return name, ""
	}
	return name[:hash], name[hash:]
}

func isHexDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= '0' && c <= '9', c >= 'a' && c <= 'f', c >= 'A' && c <= 'F':
		default:
			return false
		}
	}
	return true
}

func isDecimalDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// SanitizeForDisplay strips dangerous control/format chars but keeps
// allowed ANSI SGR (\x1b[...m) and OSC8 hyperlinks (\x1b]8;...;uri\x1b\\)
// whose target uses an allowed scheme. Every other OSC (clipboard write,
// palette/color, kitty, ...) is dropped, so a compromised server cannot
// drive the terminal beyond color and http(s) links.
func SanitizeForDisplay(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		if s[i] == 0x1b {
			// Try SGR: ESC [ ... m (whitelisted)
			if i+1 < len(s) && s[i+1] == '[' {
				j := i + 2
				for j < len(s) && s[j] != 'm' && !(s[j] >= 0x40 && s[j] <= 0x7E) {
					j++
				}
				if j < len(s) && s[j] == 'm' {
					b.WriteString(s[i : j+1])
					i = j + 1
					continue
				}
				// Not SGR — strip entire CSI sequence up to final byte (0x40-0x7E)
				for j < len(s) && !(s[j] >= 0x40 && s[j] <= 0x7E) {
					j++
				}
				if j < len(s) {
					j++ // include final byte
				}
				i = j
				continue
			}
			// OSC: only OSC8 hyperlinks are allowed, and only when their
			// target uses an allowed scheme. Anything else (ESC ]52 clipboard
			// write, ESC ]4/10/11 palette/color, kitty protocols, ...) is
			// dropped whole.
			if i+1 < len(s) && s[i+1] == ']' {
				// Look for ESC \ terminator
				j := i + 2
				found := false
				for j < len(s)-1 {
					if s[j] == 0x1b && s[j+1] == '\\' {
						found = true
						break
					}
					j++
				}
				if !found {
					// Unterminated OSC: fail closed. Everything after was
					// meant as escape payload (link target); emitting it as
					// text would leak URLs, so drop the tail.
					return b.String()
				}
				if osc8TargetAllowed(s[i+2 : j]) {
					b.WriteString(s[i : j+2])
				}
				i = j + 2
				continue
			}

			// Unknown ESC - strip it
			i++
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError {
			i += size
			continue
		}
		if r == 0xFFFD || unicode.Is(unicode.Cf, r) || unicode.Is(unicode.Mn, r) || unicode.Is(unicode.Me, r) || unicode.Is(unicode.Zl, r) || unicode.Is(unicode.Zp, r) {
			i += size
			continue
		}
		if !unicode.IsGraphic(r) && r != '\n' && r != ' ' && r != '\t' {
			i += size
			continue
		}
		b.WriteRune(r)
		i += size
	}
	return b.String()
}

// osc8TargetAllowed reports whether an OSC payload is an OSC8 hyperlink
// whose target (if any) uses an allowed scheme. The payload layout is
// "8;params;uri", with an empty uri closing the link. Only http(s) is
// allowed. Any control byte (C0 or DEL) inside the payload rejects it: the
// sequence is emitted verbatim, so a byte like BEL or a nested ESC would
// end it early and let the rest run as a fresh escape sequence.
func osc8TargetAllowed(payload string) bool {
	for i := 0; i < len(payload); i++ {
		if payload[i] < 0x20 || payload[i] == 0x7f {
			return false
		}
	}
	if len(payload) < 2 || payload[0] != '8' || payload[1] != ';' {
		return false
	}
	rest := payload[2:]
	sep := strings.IndexByte(rest, ';')
	if sep < 0 {
		return false
	}
	uri := rest[sep+1:]
	if uri == "" {
		return true // link close
	}
	lower := strings.ToLower(uri)
	return strings.HasPrefix(lower, "http://") ||
		strings.HasPrefix(lower, "https://")
}

// SanitizeSingleLine is SanitizeForDisplay for one-line fields (time,
// display name, host): newlines are removed so a crafted field cannot
// forge extra terminal lines.
func SanitizeSingleLine(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' {
			return -1
		}
		return r
	}, SanitizeForDisplay(s))
}

// CleanHistoryMessage only strips broken replacement chars, for legacy history replay.
func CleanHistoryMessage(s string) string {
	return strings.Map(func(r rune) rune {
		if r == 0xFFFD {
			return -1
		}
		return r
	}, s)
}

package filter

import (
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

var (
	ErrInvalidCharacter   = errors.New("message contains invalid character")
	ErrInvalidDisplayName = errors.New("display name contains invalid character")
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

func IsValidMessage(text string) bool { return ValidateMessage(text) == nil }

// ValidateDisplayName forbids newline and same invalid set.
func ValidateDisplayName(name string) error {
	if !utf8.ValidString(name) {
		return fmt.Errorf("%w: invalid utf8", ErrInvalidDisplayName)
	}
	for _, r := range name {
		if r == '\n' {
			return fmt.Errorf("%w: newline", ErrInvalidDisplayName)
		}
		if r == 0xFFFD {
			return fmt.Errorf("%w: U+FFFD", ErrInvalidDisplayName)
		}
		if unicode.Is(unicode.Cf, r) {
			return fmt.Errorf("%w: Cf U+%04X", ErrInvalidDisplayName, r)
		}
		if unicode.Is(unicode.Mn, r) || unicode.Is(unicode.Me, r) {
			return fmt.Errorf("%w: Mn/Me U+%04X", ErrInvalidDisplayName, r)
		}
		if unicode.Is(unicode.Zl, r) || unicode.Is(unicode.Zp, r) {
			return fmt.Errorf("%w: Zl/Zp U+%04X", ErrInvalidDisplayName, r)
		}
		if !unicode.IsGraphic(r) && r != ' ' {
			return fmt.Errorf("%w: non-graphic U+%04X", ErrInvalidDisplayName, r)
		}
	}
	return nil
}

// SanitizeForDisplay strips dangerous control/format chars but keeps
// allowed ANSI SGR (\x1b[...m) and OSC8 (\x1b]8;;...\x1b\\) that server generates.
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
			// Try OSC8: ESC ]8;; ... ESC \
			if i+1 < len(s) && s[i+1] == ']' {
				// Look for ESC \ terminator
				j := i + 2
				found := false
				for j < len(s)-1 {
					if s[j] == 0x1b && s[j+1] == '\\' {
						b.WriteString(s[i : j+2])
						i = j + 2
						found = true
						break
					}
					j++
				}
				if found {
					continue
				}
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

// CleanHistoryMessage only strips broken replacement chars, for legacy history replay.
func CleanHistoryMessage(s string) string {
	return strings.Map(func(r rune) rune {
		if r == 0xFFFD {
			return -1
		}
		return r
	}, s)
}

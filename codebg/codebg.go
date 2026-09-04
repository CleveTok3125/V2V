// Package codebg renders markdown-style code spans in chat text with a
// distinct terminal background, mirroring how markdown shows inline code
// and fenced blocks. Like linkify, it only inserts zero-width SGR
// sequences, so visible characters (and display-cell arithmetic in line
// editors) are unchanged.
package codebg

import "strings"

const (
	sgrCodeBg = "\x1b[48;5;236m"
	sgrBgOff  = "\x1b[49m"
)

// NeedsContinuation reports whether a first input line that opens a fenced
// block still needs more input lines to close the fence. It mirrors the
// toggle semantics of Render: a first line that already contains a closing
// fence after the opener is complete and must not open multiline
// collection.
func NeedsContinuation(firstLine string) bool {
	t := strings.TrimSpace(firstLine)
	if !strings.HasPrefix(t, "```") {
		return false
	}
	return !strings.Contains(t[3:], "```")
}

// Render returns text with code spans wrapped in a background color.
// Single backtick pairs on one line (`code`) and fenced blocks (```)
// are supported; the backticks themselves are kept visible. Fence marker
// lines pass through unchanged, content lines inside a fence get the
// background on the whole line. Unmatched backticks, empty spans and text
// already containing ESC sequences are returned untouched.
func Render(text string) string {
	if text == "" || !strings.Contains(text, "`") || strings.Contains(text, "\x1b") {
		return text
	}
	lines := strings.Split(text, "\n")
	inFence := false
	for i, line := range lines {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "```") {
			rest := t[3:]
			if idx := strings.Index(rest, "```"); idx >= 0 && strings.TrimSpace(rest[:idx]+rest[idx+3:]) != "" {
				// Single-line fenced span: ```code``` gets the background.
				lines[i] = sgrCodeBg + line + sgrBgOff
			} else if rest == "" || isWord(rest) {
				// Pure fence marker (``` or ```go): toggle block state.
				inFence = !inFence
			} else {
				// Fence run glued to other text: treat as inline backticks.
				lines[i] = inline(line)
			}
			continue
		}
		if inFence {
			lines[i] = sgrCodeBg + line + sgrBgOff
			continue
		}
		lines[i] = inline(line)
	}
	return strings.Join(lines, "\n")
}

// isWord reports whether s is a bare language tag (letters, digits,
// plus, minus), e.g. the "go" in "```go".
func isWord(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '+', r == '-':
		default:
			return false
		}
	}
	return true
}

// inline wraps single-backtick pairs on one line, matched sequentially
// from the left. A trailing unmatched backtick is left literal, so no
// delimiter is ever swallowed.
func inline(line string) string {
	if strings.Count(line, "`") < 2 {
		return line
	}
	var b strings.Builder
	rest := line
	for {
		open := strings.Index(rest, "`")
		if open < 0 {
			b.WriteString(rest)
			break
		}
		rel := strings.Index(rest[open+1:], "`")
		if rel < 0 {
			b.WriteString(rest)
			break
		}
		content := rest[open+1 : open+1+rel]
		if content == "" {
			b.WriteString(rest[:open+2])
		} else {
			b.WriteString(rest[:open])
			b.WriteString(sgrCodeBg + "`" + content + "`" + sgrBgOff)
		}
		rest = rest[open+1+rel+1:]
	}
	return b.String()
}

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

// Render returns text with code spans wrapped in a background color,
// mirroring markdown: inline `code` spans lose their backticks, a fenced
// block shows its language name as a header line followed by the block,
// and fence markers never appear in the output. Unmatched backticks,
// empty spans and text already containing ESC sequences are returned
// untouched.
func Render(text string) string {
	if text == "" || !strings.Contains(text, "`") || strings.Contains(text, "\x1b") {
		return text
	}
	lines := strings.Split(text, "\n")
	var out []string
	inFence := false
	for _, line := range lines {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "```") {
			rest := strings.TrimSpace(t[3:])
			if lang, inner, ok := singleLineFence(rest); ok {
				// Complete fence on one line: ```code``` or ```go x```.
				// A language tag earns a header line like a block does.
				if lang != "" {
					out = append(out, sgrCodeBg+lang+sgrBgOff)
				}
				out = append(out, sgrCodeBg+inner+sgrBgOff)
				continue
			}
			if rest == "" {
				// Bare opener/closer: toggle block state, markers hidden.
				inFence = !inFence
				continue
			}
			if !inFence {
				if lang := fenceLang(rest); lang != "" {
					// Opening fence with language: header line, then block.
					inFence = true
					out = append(out, sgrCodeBg+lang+sgrBgOff)
					continue
				}
			}
			// Anything else starting with a fence run (a closer with
			// trailing text, or a run glued to prose): treat the whole
			// line as inline backticks.
			out = append(out, inline(line))
			continue
		}
		if inFence {
			out = append(out, sgrCodeBg+line+sgrBgOff)
			continue
		}
		out = append(out, inline(line))
	}
	return strings.Join(out, "\n")
}

// singleLineFence parses rest (the text after an opening ``` on a line
// that contains another ``` run) as a self-contained fenced span. It
// returns the language tag ("" when absent), the inner content and true
// when the remainder holds a closer with only spaces after it. A tag
// alone with nothing after it (```go```) counts as content, not as a
// language header, so the markers truly wrap something.
func singleLineFence(rest string) (lang, inner string, ok bool) {
	idx := strings.Index(rest, "```")
	if idx < 0 {
		return "", "", false
	}
	if tail := strings.TrimSpace(rest[idx+3:]); tail != "" {
		return "", "", false
	}
	head := strings.TrimSpace(rest[:idx])
	if head == "" {
		return "", "", false
	}
	if tag := fenceLang(head); tag != "" {
		if content := strings.TrimSpace(strings.TrimPrefix(head, tag)); content != "" {
			return tag, content, true
		}
	}
	return "", head, true
}

// fenceLang extracts a bare language tag (letters, digits, plus, minus)
// heading the rest of a fence line, e.g. the "go" in "```go".
func fenceLang(rest string) string {
	fields := strings.Fields(rest)
	if len(fields) == 0 || !isWord(fields[0]) {
		return ""
	}
	return fields[0]
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
// from the left, and strips the delimiters like markdown does. A trailing
// unmatched backtick and empty spans are left literal, so no delimiter is
// ever swallowed.
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
			b.WriteString(sgrCodeBg + content + sgrBgOff)
		}
		rest = rest[open+1+rel+1:]
	}
	return b.String()
}

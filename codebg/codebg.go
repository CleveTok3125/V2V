// Package codebg renders markdown-style code spans in chat text with a
// distinct terminal background, mirroring how markdown shows inline code
// and fenced blocks. Like linkify, it only inserts zero-width SGR
// sequences, so display-cell arithmetic in line editors is unchanged.
package codebg

import (
	"bytes"
	"fmt"
	"strings"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/lexers"
)

const (
	sgrCodeBg = "\x1b[48;5;236m"
	sgrBgOff  = "\x1b[49m"
)

// maxHighlightBytes bounds a single highlighted block. Chat messages are
// already capped by MaxMessageLength, so this is only defence in depth.
const maxHighlightBytes = 64 * 1024

// Style is a syntax highlight palette: truecolor [r,g,b] triples.
// A [0,0,0] entry means "use the default".
type Style struct {
	Background [3]int
	Keyword    [3]int
	String     [3]int
	Comment    [3]int
	Number     [3]int
	Name       [3]int
	Function   [3]int
	Type       [3]int
	Operator   [3]int
}

// DefaultStyle matches the compiled look: dark background with monokai-ish
// token colours.
func DefaultStyle() Style {
	return Style{
		Background: [3]int{48, 48, 48},
		Keyword:    [3]int{255, 183, 77},
		String:     [3]int{174, 213, 129},
		Comment:    [3]int{144, 164, 174},
		Number:     [3]int{255, 213, 79},
		Name:       [3]int{100, 181, 246},
		Function:   [3]int{77, 208, 225},
		Type:       [3]int{149, 117, 205},
		Operator:   [3]int{216, 222, 233},
	}
}

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
	return render(text, nil)
}

// RenderWithStyle is Render with syntax highlighting for fenced blocks
// that carry a known language tag. Inline spans and blocks without a
// known tag keep the plain background, so unknown languages degrade to
// exactly what Render produces. A nil highlight entry falls back to the
// plain background.
func RenderWithStyle(text string, st Style) string {
	return render(text, func(code, lang string) (string, bool) {
		return highlightBlock(code, lang, st)
	})
}

// render implements Render and RenderWithStyle. The highlight hook turns
// a fenced block's joined content and language tag into highlighted
// output; a nil hook (or a false return) keeps the plain background.
func render(text string, highlight func(code, lang string) (string, bool)) string {
	if text == "" || !strings.Contains(text, "`") || strings.Contains(text, "\x1b") {
		return text
	}
	lines := strings.Split(text, "\n")
	var out []string
	var block []string
	blockLang := ""
	inFence := false
	// flush highlights the collected block lines as one unit so token
	// colours stay consistent, then resumes plain output. An empty block
	// (adjacent opener/closer) emits nothing, like Render.
	flush := func() {
		if !inFence {
			return
		}
		if len(block) > 0 {
			out = append(out, highlightOrBg(strings.Join(block, "\n"), blockLang, highlight)...)
		}
		block = nil
		blockLang = ""
	}
	for _, line := range lines {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "```") {
			rest := strings.TrimSpace(t[3:])
			if lang, inner, ok := singleLineFence(rest); ok {
				// Complete fence on one line: ```code``` or ```go x```.
				// A language tag earns a header line like a block does.
				// The fence state is untouched, matching Render: inside
				// a block this is just another content line, so flush
				// first to keep output order.
				flush()
				if lang != "" {
					out = append(out, sgrCodeBg+lang+sgrBgOff)
				}
				out = append(out, highlightOrBg(inner, lang, highlight)...)
				continue
			}
			if rest == "" {
				// Bare opener/closer: toggle block state, markers hidden.
				if inFence {
					flush()
					inFence = false
				} else {
					inFence = true
				}
				continue
			}
			if !inFence {
				if lang := fenceLang(rest); lang != "" {
					// Opening fence with language: header line, then block.
					inFence = true
					blockLang = lang
					out = append(out, sgrCodeBg+lang+sgrBgOff)
					continue
				}
			}
			// Anything else starting with a fence run (a closer with
			// trailing text, or a run glued to prose): treat the whole
			// line as inline backticks.
			flush()
			out = append(out, inline(line))
			continue
		}
		if inFence {
			block = append(block, line)
			continue
		}
		out = append(out, inline(line))
	}
	// An unclosed fence highlights to the end of the message.
	flush()
	return strings.Join(out, "\n")
}

// highlightOrBg highlights code with the hook and splits the result back
// into lines, or falls back to the plain per-line background when the
// hook is nil or declines (unknown language, oversize block, lex error).
func highlightOrBg(code, lang string, highlight func(code, lang string) (string, bool)) []string {
	if highlight != nil {
		if h, ok := highlight(code, lang); ok {
			return strings.Split(h, "\n")
		}
	}
	lines := strings.Split(code, "\n")
	for i, l := range lines {
		lines[i] = sgrCodeBg + l + sgrBgOff
	}
	return lines
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

// highlightBlock highlights one fenced block with chroma and returns the
// result split-ready (no trailing newline). Every emitted line is wrapped
// in its own SGR open/reset pair split at embedded newlines, so terminal
// line filters and per-line tab buffers stay self-contained. It reports
// false for empty/unknown language tags, oversize blocks and lex errors;
// callers then keep the plain background.
func highlightBlock(code, lang string, st Style) (string, bool) {
	lang = strings.TrimSpace(lang)
	if lang == "" || len(code) > maxHighlightBytes {
		return "", false
	}
	lexer := lexers.Get(lang)
	if lexer == nil {
		return "", false
	}
	it, err := chroma.Coalesce(lexer).Tokenise(nil, code)
	if err != nil {
		return "", false
	}
	style := buildChromaStyle(st)
	bgTriple := st.Background
	if bgTriple == ([3]int{}) {
		bgTriple = DefaultStyle().Background
	}
	bg := fmt.Sprintf("\x1b[48;2;%d;%d;%dm", bgTriple[0], bgTriple[1], bgTriple[2])
	var buf bytes.Buffer
	for token := it(); token != chroma.EOF; token = it() {
		entry := style.Get(token.Type)
		fg := ""
		if entry.Colour.IsSet() {
			c := entry.Colour
			fg = fmt.Sprintf("\x1b[38;2;%d;%d;%dm", c.Red(), c.Green(), c.Blue())
		}
		parts := strings.Split(token.Value, "\n")
		for i, part := range parts {
			if i > 0 {
				buf.WriteString("\x1b[0m\n")
			}
			buf.WriteString(fg)
			buf.WriteString(bg)
			buf.WriteString(part)
			buf.WriteString("\x1b[0m")
		}
	}
	return buf.String(), true
}

// buildChromaStyle converts a palette into a chroma style. A [0,0,0]
// entry falls back to DefaultStyle so partial user palettes still colour
// every token class. chroma requires a Background entry, which carries
// the block background.
func buildChromaStyle(st Style) *chroma.Style {
	d := DefaultStyle()
	hex := func(v, fb [3]int) string {
		if v == ([3]int{}) {
			v = fb
		}
		return fmt.Sprintf("#%02x%02x%02x", clampChan(v[0]), clampChan(v[1]), clampChan(v[2]))
	}
	return chroma.MustNewStyle("v2v", chroma.StyleEntries{
		chroma.Background:   "bg:" + hex(st.Background, d.Background),
		chroma.Keyword:      hex(st.Keyword, d.Keyword),
		chroma.KeywordType:  hex(st.Type, d.Type),
		chroma.Name:         hex(st.Name, d.Name),
		chroma.NameFunction: hex(st.Function, d.Function),
		chroma.NameClass:    hex(st.Type, d.Type),
		chroma.LiteralString: hex(st.String, d.String),
		chroma.Comment:      hex(st.Comment, d.Comment),
		chroma.Number:       hex(st.Number, d.Number),
		chroma.Operator:     hex(st.Operator, d.Operator),
	})
}

func clampChan(v int) int {
	if v < 0 {
		return 0
	}
	if v > 255 {
		return 255
	}
	return v
}

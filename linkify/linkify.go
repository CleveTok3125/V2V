// Package linkify wraps plain http(s) URLs in chat text with OSC 8
// hyperlink escapes plus SGR styling, so terminals and xterm.js render a
// clickable, underlined, bright-blue link. The visible characters are
// unchanged, which keeps display-cell arithmetic in line editors intact.
package linkify

import (
	"regexp"
	"strings"
)

// urlRe matches absolute http(s) URLs. ESC bytes are excluded so an escape
// sequence already present in the payload can never be swallowed, and a
// scheme is required so timestamps or plain prose are never matched.
var urlRe = regexp.MustCompile(`https?://[^\s\x1b]+`)

const (
	osc8Open  = "\x1b]8;;"
	osc8Close = "\x1b\\"
	sgrLink   = "\x1b[94;4m"
	sgrReset  = "\x1b[0m"
)

// trailingPunct is trimmed off the end of a match and kept outside the
// clickable region, so sentences ending in a URL stay readable.
const trailingPunct = ".,;:!?)»\"'’”…"

// Linkify returns text with every http(s) URL wrapped in an OSC 8 hyperlink.
// Text that already contains hyperlinks is returned untouched: call sites
// format each message exactly once, and user input cannot contain ESC.
func Linkify(text string) string {
	if !strings.Contains(text, "http") || strings.Contains(text, osc8Open) {
		return text
	}
	return urlRe.ReplaceAllStringFunc(text, func(m string) string {
		url := strings.TrimRight(m, trailingPunct)
		tail := m[len(url):]
		return osc8Open + url + osc8Close + sgrLink + url + sgrReset + osc8Open + osc8Close + tail
	})
}

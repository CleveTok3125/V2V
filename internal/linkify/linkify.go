// Package linkify wraps plain http(s) URLs in chat text with OSC 8
// hyperlink escapes plus SGR styling, so terminals and xterm.js render a
// clickable, underlined, bright-blue link. The visible characters are
// unchanged, which keeps display-cell arithmetic in line editors intact.
package linkify

import (
	"regexp"
	"strings"

	"mvdan.cc/xurls"
)

// strictHTTP matches absolute http(s) URLs with xurls Strict-class
// boundaries, restricted to the http(s) schemes this package wraps:
// plain xurls.Strict would also link ftp:// and other schemes. ESC
// bytes can never match (outside the character classes), so an escape
// sequence already present in the payload is never swallowed, and a
// scheme is required so timestamps or plain prose never match.
var strictHTTP = mustStrictHTTP()

func mustStrictHTTP() *regexp.Regexp {
	re, err := xurls.StrictMatchingScheme("https?")
	if err != nil {
		panic("linkify: invalid http scheme pattern: " + err.Error())
	}
	return re
}

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
	return strictHTTP.ReplaceAllStringFunc(text, func(m string) string {
		url := strings.TrimRight(m, trailingPunct)
		tail := m[len(url):]
		return Wrap(url, url) + tail
	})
}

// Wrap renders visible text as a hyperlink to url in exactly the shape
// Linkify uses, so markup [text](url) matches bare-URL styling.
func Wrap(url, visible string) string {
	return osc8Open + url + osc8Close + sgrLink + visible + sgrReset + osc8Open + osc8Close
}

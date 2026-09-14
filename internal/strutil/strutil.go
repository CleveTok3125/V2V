// Package strutil holds one-line string helpers shared by the server
// and admin binaries (both are package main, so they cannot share
// unexported code).
package strutil

// Short truncates s for log lines: full string up to 12 chars, then
// the head plus an ellipsis. Short inputs pass through untouched, so
// attacker-controlled or corrupt short strings never panic a slice.
func Short(s string) string {
	return ShortN(s, 12)
}

// ShortN is Short with an explicit width. Width counts runes (never
// bytes), so multibyte input is cut on a rune boundary instead of
// mid-UTF-8-sequence. Widths <= 0 return s as-is (no truncation
// requested); short inputs still pass through, so the result never
// overruns len(s).
func ShortN(s string, n int) string {
	if n <= 0 {
		return s
	}
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n]) + "…"
}

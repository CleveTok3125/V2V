// Package strutil holds one-line string helpers shared by the server
// and admin binaries (both are package main, so they cannot share
// unexported code).
package strutil

// Short truncates s for log lines: full string up to 12 chars, then
// the head plus an ellipsis. Short inputs pass through untouched, so
// attacker-controlled or corrupt short strings never panic a slice.
func Short(s string) string {
	if len(s) > 12 {
		return s[:12] + "…"
	}
	return s
}

package main

import "strings"

// Slash command handling.
//
// The dispatch in client.go matches every built-in command first, so
// what reaches isUnknownSlashCommand is input starting with "/" that
// matched nothing: always a mistyped command, rejected locally, never
// broadcast or trip-signed.
//
// Code blocks (```) never reach here: joined before dispatch, they
// always start with backticks.

// isUnknownSlashCommand reports whether text is a mistyped command: it
// starts with "/" and matched no known command. Callers must only pass
// text that the known-command dispatch already skipped; text is expected
// trimmed (no leading/trailing spaces).
func isUnknownSlashCommand(text string) bool {
	return strings.HasPrefix(text, "/")
}

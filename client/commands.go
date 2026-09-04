package main

import "strings"

// Slash command handling.
//
// Every built-in command is matched by the dispatch in client.go before
// anything below runs (/help, /quit, /tab 1, /verify...). What reaches
// isUnknownSlashCommand is therefore input starting with "/" that matched
// no known command — a mistyped command. It is always rejected locally so
// a typo is never broadcast or trip-signed.
//
// Code blocks (```) never reach here: they are joined before dispatch and
// always start with backticks.

// isUnknownSlashCommand reports whether text is a mistyped command: it
// starts with "/" and matched no known command. Callers must only pass
// text that the known-command dispatch already skipped; text is expected
// trimmed (no leading/trailing spaces).
func isUnknownSlashCommand(text string) bool {
	return strings.HasPrefix(text, "/")
}

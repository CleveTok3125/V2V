package main

import "strings"

// Slash command handling.
//
// Every built-in command is written contiguously (no space inside the
// command itself): /help, /quit, /tab 1 and /verify... are matched by the
// dispatch in client.go before anything below runs. What reaches
// slashFallbackSend is therefore input starting with "/" that matched no
// known command:
//
//   - contains a space (e.g. "/hello world") → ordinary chat message,
//     sent as-is with the slash kept;
//   - no space (e.g. "/halp", "/tab1", "/") → unknown command, rejected
//     locally so a typo is never broadcast.
//
// Code blocks (```) never reach here: they are joined before dispatch and
// always start with backticks.

// slashFallbackSend reports whether unknown slash input should be sent as
// chat (true, it contains a space) or rejected as an unknown command.
// Callers must only pass text that starts with "/" and matched no known
// command; text is expected trimmed (no leading/trailing spaces).
func slashFallbackSend(text string) bool {
	if !strings.HasPrefix(text, "/") {
		return true
	}
	return strings.Contains(text, " ")
}

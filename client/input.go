package main

import "errors"

// ErrInputCancel aborts the in-flight ReadLine without submitting it.
// Backends translate their native interrupt into this sentinel so the chat
// loop can tell "user canceled" apart from a real stream error: cancel
// discards partial input and stays in chat, while io.EOF still quits.
var ErrInputCancel = errors.New("input canceled")

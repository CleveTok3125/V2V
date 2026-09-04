//go:build !js

package main

import (
	"errors"
	"io"
	"strings"

	"github.com/chzyer/readline"
)

type readlineTerm struct {
	rl *readline.Instance
}

func newInputTerminal() (inputTerminal, error) {
	rl, err := readline.NewEx(&readline.Config{
		Prompt:          "| > ",
		HistoryFile:     historyFile,
		InterruptPrompt: "^C",
		EOFPrompt:       "/quit",
	})
	if err != nil {
		return nil, err
	}
	return &readlineTerm{rl: rl}, nil
}

func (t *readlineTerm) ReadLine() (string, error) {
	s, err := t.rl.Readline()
	if errors.Is(err, readline.ErrInterrupt) {
		// Two-stage Ctrl+C like the WASM editor: readline already cleared
		// the line, so a non-empty partial just ends the read silently
		// and only an empty line cancels with a hint upstream.
		if strings.TrimSpace(s) != "" {
			return "", nil
		}
		return "", ErrInputCancel
	}
	return s, err
}

func (t *readlineTerm) SetPrompt(p string) { t.rl.SetPrompt(p) }

func (t *readlineTerm) Refresh() { t.rl.Refresh() }

func (t *readlineTerm) Close() { t.rl.Close() }

func (t *readlineTerm) Writer() io.Writer { return t.rl.Stdout() }

// notifyQuit lets the platform hook do any cleanup when the client quits.
// On desktop the process exits naturally; nothing extra is needed.
func notifyQuit() {}

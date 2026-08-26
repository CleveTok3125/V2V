//go:build !js

package main

import (
	"io"

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

func (t *readlineTerm) ReadLine() (string, error) { return t.rl.Readline() }

func (t *readlineTerm) SetPrompt(p string) { t.rl.SetPrompt(p) }

func (t *readlineTerm) Refresh() { t.rl.Refresh() }

func (t *readlineTerm) Close() { t.rl.Close() }

func (t *readlineTerm) Writer() io.Writer { return t.rl.Stdout() }

// notifyQuit lets the platform hook do any cleanup when the client quits.
// On desktop the process exits naturally; nothing extra is needed.
func notifyQuit() {}

//go:build !js

package main

import (
	"errors"
	"fmt"
	"io"
	"os"
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
	hardenHistoryFile()
	return &readlineTerm{rl: rl}, nil
}

// hardenHistoryFile forces history.tmp to owner-only. readline creates it
// with the process umask default (often 0644), exposing typed lines to
// other local users. Best effort: a failure warns instead of aborting
// startup, since the file still works.
func hardenHistoryFile() {
	if historyFile == "" {
		return
	}
	if err := os.Chmod(historyFile, 0o600); err != nil {
		if !os.IsNotExist(err) {
			fmt.Printf("cảnh báo: không đặt được quyền 0600 cho %s: %v\n", historyFile, err)
		}
		return
	}
	if fi, err := os.Stat(historyFile); err == nil && fi.Mode().Perm()&0o077 != 0 {
		fmt.Printf("cảnh báo: %s vẫn cho nhóm/người khác đọc (mode %o)\n", historyFile, fi.Mode().Perm())
	}
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

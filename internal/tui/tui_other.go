//go:build !js

package tui

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/charmbracelet/huh"
	xterm "github.com/charmbracelet/x/term"
)

// Interactive reports whether the full-screen huh forms can run:
// stdin must be a real terminal. Callers branch to the piped readers
// otherwise.
func Interactive() bool {
	return xterm.IsTerminal(os.Stdin.Fd())
}

// Confirm asks a yes/no question. TTY sessions get a huh form with
// the bound value starting false, so focus sits on Không: default No
// by construction. Otherwise one plain line from stdin; aborting the
// form is an error, never a silent answer.
func Confirm(title string) (bool, error) {
	if Interactive() {
		var yes bool
		form := huh.NewForm(huh.NewGroup(
			huh.NewConfirm().Title(title).Affirmative("Có").Negative("Không").Value(&yes),
		))
		if err := form.Run(); err != nil {
			if errors.Is(err, huh.ErrUserAborted) {
				return false, ErrAborted
			}
			return false, err
		}
		return yes, nil
	}
	fmt.Print(title + " (y/N): ")
	return ConfirmPiped(os.Stdin), nil
}

// Select asks for one of options. TTY sessions get a huh select with
// the cursor preset on options[def]; otherwise a printed numbered
// menu. Both return the 0-based index; empty, invalid, or unreadable
// piped input means def.
func Select(title string, options []string, def int) (int, error) {
	if len(options) == 0 {
		return 0, errors.New("select cần ít nhất một lựa chọn")
	}
	if def < 0 || def >= len(options) {
		def = 0
	}
	if Interactive() {
		return runSelect(title, options, def)
	}
	return SelectPiped(os.Stdin, title, options, def)
}

func runSelect(title string, options []string, def int) (int, error) {
	choice := options[def]
	form := huh.NewForm(huh.NewGroup(
		huh.NewSelect[string]().Title(title).Options(selectOptions(options)...).Value(&choice),
	))
	if err := form.Run(); err != nil {
		if errors.Is(err, huh.ErrUserAborted) {
			return 0, ErrAborted
		}
		return 0, err
	}
	for i, opt := range options {
		if opt == choice {
			return i, nil
		}
	}
	return def, nil
}

func selectOptions(options []string) []huh.Option[string] {
	out := make([]huh.Option[string], 0, len(options))
	for _, opt := range options {
		out = append(out, huh.NewOption(opt, opt))
	}
	return out
}

// SelectPiped is the non-TTY fallback: a printed numbered menu with
// the same choice semantics as the huh select.
func SelectPiped(r io.Reader, title string, options []string, def int) (int, error) {
	if len(options) == 0 {
		return 0, errors.New("select cần ít nhất một lựa chọn")
	}
	if def < 0 || def >= len(options) {
		def = 0
	}
	fmt.Println(title)
	for i, opt := range options {
		fmt.Printf("  [%d] %s\n", i+1, opt)
	}
	fmt.Printf("Chọn (1-%d, Enter = %d): ", len(options), def+1)
	line, err := ReadLine(r)
	if err != nil && len(line) == 0 {
		return def, nil
	}
	n, err := strconv.Atoi(strings.TrimSpace(line))
	if err != nil || n < 1 || n > len(options) {
		return def, nil
	}
	return n - 1, nil
}

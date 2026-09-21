package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/mattn/go-isatty"
	"github.com/muesli/termenv"
)

// pageThresholdLines bounds direct TTY output. Longer text goes through
// $PAGER so unified diffs stay readable instead of flooding the terminal.
const pageThresholdLines = 30

// emitPaged writes text to stdout directly or through a pager. Diff output
// is colorized unless the terminal declines color (pipe, NO_COLOR, dumb).
func emitPaged(text string, noPager bool) {
	if noPager || !wantPager(text, stdoutIsTTY()) {
		fmt.Print(colorizeDiff(text))
		return
	}
	if _, _, disabled := pagerCommand(); disabled {
		fmt.Print(colorizeDiff(text))
		return
	}
	if err := runPager(colorizeDiff(text)); err != nil {
		fmt.Print(colorizeDiff(text))
	}
}

// colorizeDiff paints unified diff lines when the terminal supports color.
// Pipes, NO_COLOR and dumb terminals stay plain.
func colorizeDiff(text string) string {
	if termenv.NewOutput(os.Stdout).EnvColorProfile() == termenv.Ascii {
		return text
	}
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		lines[i] = paintDiffLine(line)
	}
	return strings.Join(lines, "\n")
}

// paintDiffLine colors one diff line unconditionally (pure, for tests).
func paintDiffLine(line string) string {
	switch {
	case strings.HasPrefix(line, "+++") || strings.HasPrefix(line, "---"):
		return termenv.String(line).Bold().String()
	case strings.HasPrefix(line, "@@"):
		return termenv.String(line).Foreground(termenv.ANSICyan).String()
	case strings.HasPrefix(line, "+"):
		return termenv.String(line).Foreground(termenv.ANSIGreen).String()
	case strings.HasPrefix(line, "-"):
		return termenv.String(line).Foreground(termenv.ANSIRed).String()
	default:
		return line
	}
}

// wantPager is the pure paging decision for tests.
func wantPager(text string, isTTY bool) bool {
	if !isTTY {
		return false
	}
	return strings.Count(text, "\n") > pageThresholdLines
}

func stdoutIsTTY() bool {
	return isatty.IsTerminal(os.Stdout.Fd())
}

// pagerCommand resolves $PAGER with safe field splitting. Empty means the
// less default; "cat" disables paging.
func pagerCommand() (name string, args []string, disabled bool) {
	raw := strings.TrimSpace(os.Getenv("PAGER"))
	if raw == "" {
		return "less", []string{"-FRX"}, false
	}
	fields := strings.Fields(raw)
	if len(fields) == 0 {
		return "less", []string{"-FRX"}, false
	}
	if fields[0] == "cat" {
		return "", nil, true
	}
	return fields[0], fields[1:], false
}

func runPager(text string) error {
	name, args, disabled := pagerCommand()
	if disabled {
		fmt.Print(text)
		return nil
	}
	path, err := exec.LookPath(name)
	if err != nil {
		return err
	}
	cmd := exec.Command(path, args...)
	cmd.Stdin = strings.NewReader(text)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

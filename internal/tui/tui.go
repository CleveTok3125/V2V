// Package tui is the general terminal UI kit: confirmations and
// selections. Interactive TTY sessions get huh forms; piped or
// non-TTY input falls back to plain line reads with identical choice
// semantics so scripts and tests keep working. Password entry stays
// out: that specialty belongs to internal/passprompt, which imports
// the TTY detector from here.
package tui

import (
	"errors"
	"io"
	"strings"
)

var (
	// ErrAborted reports Ctrl+C / Esc instead of an answer.
	ErrAborted = errors.New("đã hủy nhập")
)

// confirmYes is the exact accept set of the legacy y/N prompts:
// an explicit yes only, everything else (empty, errors, "n") is No.
func confirmYes(line string) bool {
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes", "có", "co":
		return true
	default:
		return false
	}
}

// ConfirmPiped parses one y/N answer from r with no printing.
// Default is No: unreadable input also means No.
func ConfirmPiped(r io.Reader) bool {
	line, err := ReadLine(r)
	if err != nil && len(line) == 0 {
		return false
	}
	return confirmYes(line)
}

// ReadLine reads one line without read-ahead: byte-by-byte, so bytes
// meant for later readers stay on the fd. Buffered readers would
// swallow piped input past the newline.
func ReadLine(r io.Reader) (string, error) {
	var buf []byte
	one := make([]byte, 1)
	for {
		n, err := r.Read(one)
		if n > 0 {
			if one[0] == '\n' {
				break
			}
			buf = append(buf, one[0])
		}
		if err != nil {
			if len(buf) == 0 {
				return "", err
			}
			break
		}
	}
	return strings.TrimRight(string(buf), "\r"), nil
}

// Package passprompt is the single shared secret-entry component for
// the repo: hidden password input with optional live strength meter,
// double-entry confirmation, and default-No confirmation. Interactive
// TTY sessions get a Bubble Tea program (per-keystroke meter updates,
// which huh cannot do); piped or non-TTY input falls back to plain
// line reads with identical semantics so scripts and tests keep
// working. The package never imports zxcvbn: callers supply an
// Assessment callback, keeping the meter display-only.
package passprompt

import (
	"errors"
	"fmt"
	"io"
	"strings"
)

var (
	// ErrMismatch aborts double-entry after the round budget is spent.
	ErrMismatch = errors.New("không khớp sau 3 lần nhập")
	// ErrEmpty rejects a blank secret where emptiness is not allowed.
	ErrEmpty = errors.New("mật khẩu trống")
	// ErrAborted reports Ctrl+C / Esc instead of a value.
	ErrAborted = errors.New("đã hủy nhập")
)

// DefaultMaxRounds bounds double-entry rounds (typos + mismatches).
const DefaultMaxRounds = 3

// Assessment is the caller-computed meter snapshot for one input value.
// Bits is capped display entropy, Label its human band, Weak whether
// the caller treats the value as too weak.
type Assessment struct {
	Bits  float64
	Label string
	Weak  bool
}

// PasswordOpts tunes Password. Title and ConfirmTitle are printed
// verbatim as prompts. Confirm enables double-entry; AllowEmpty lets
// an empty first entry return immediately (no confirm round).
// Assess, when non-nil, renders the live meter line.
type PasswordOpts struct {
	Title        string
	ConfirmTitle string
	Confirm      bool
	AllowEmpty   bool
	MaxRounds    int
	Assess       func(string) Assessment
	// Expect switches to confirm-against-known-value mode: the caller
	// already holds the value (e.g. entered moments ago) and only
	// asks the user to repeat it. Confirm/Assess are ignored; the
	// meter never shows in this mode.
	Expect string
}

func (o PasswordOpts) rounds() int {
	if o.MaxRounds > 0 {
		return o.MaxRounds
	}
	return DefaultMaxRounds
}

// FormatBits renders entropy bits: whole when integral, one decimal
// otherwise. Shared by the live meter and static display lines so both
// read identically.
func FormatBits(bits float64) string {
	if bits == float64(int(bits)) {
		return fmt.Sprintf("%d", int(bits))
	}
	return fmt.Sprintf("%.1f", bits)
}

// MeterBar builds the ASCII strength bar. frac is fill in [0,1];
// values outside clamp. Width is fixed so the bar never reflows as
// the user types.
func MeterBar(frac float64) string {
	const width = 10
	if frac < 0 {
		frac = 0
	}
	if frac > 1 {
		frac = 1
	}
	full := int(frac*width + 0.5)
	return strings.Repeat("█", full) + strings.Repeat("░", width-full)
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

// OnPrompt prints round prompts for piped double-entry. first selects
// the entry vs confirmation title; round is 1-based.
type OnPrompt func(first bool, round, max int)

// SinglePiped reads one secret with no printing. Empty input is
// rejected unless allowEmpty.
func SinglePiped(read func() (string, error), allowEmpty bool) (string, error) {
	v, err := read()
	if err != nil {
		return "", err
	}
	if !allowEmpty && strings.TrimSpace(v) == "" {
		return "", ErrEmpty
	}
	return v, nil
}

// DoubleEntryPiped reads an entry plus confirmation for up to
// maxRounds (<=0 means DefaultMaxRounds). Mismatches and blank pairs
// restart the round; exhaustion aborts. onPrompt, when non-nil, prints
// prompts; nil keeps the function silent for tests.
func DoubleEntryPiped(read func() (string, error), maxRounds int, onPrompt OnPrompt) (string, error) {
	if maxRounds <= 0 {
		maxRounds = DefaultMaxRounds
	}
	var lastErr error
	for round := 1; round <= maxRounds; round++ {
		if onPrompt != nil {
			onPrompt(true, round, maxRounds)
		}
		first, err := read()
		if err != nil {
			return "", err
		}
		if onPrompt != nil {
			onPrompt(false, round, maxRounds)
		}
		second, err := read()
		if err != nil {
			return "", err
		}
		if first != second {
			lastErr = ErrMismatch
			continue
		}
		if strings.TrimSpace(first) == "" {
			lastErr = ErrEmpty
			continue
		}
		return first, nil
	}
	if lastErr != nil {
		return "", lastErr
	}
	return "", ErrMismatch
}

// ExpectPiped reads lines until one equals expect, up to maxRounds
// (<=0 means DefaultMaxRounds). A read error aborts; exhaustion
// returns ErrMismatch. onPrompt, when non-nil, prints before each
// round; nil keeps the function silent for tests.
func ExpectPiped(read func() (string, error), expect string, maxRounds int, onPrompt func(round, max int)) (string, error) {
	if maxRounds <= 0 {
		maxRounds = DefaultMaxRounds
	}
	for round := 1; round <= maxRounds; round++ {
		if onPrompt != nil {
			onPrompt(round, maxRounds)
		}
		v, err := read()
		if err != nil {
			return "", err
		}
		if v == expect {
			return v, nil
		}
	}
	return "", ErrMismatch
}

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

// ConfirmPiped parses one y/N answer with no printing. Default is No:
// unreadable input also means No.
func ConfirmPiped(r io.Reader) bool {
	line, err := ReadLine(r)
	if err != nil && len(line) == 0 {
		return false
	}
	return confirmYes(line)
}

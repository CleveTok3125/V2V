package main

// Passphrase strength assessment for tripcode secrets.
//
// The meter is zxcvbn (score 0-4 + entropy bits); thresholds are grounded
// in the project's KDF (argon2id, native t=3/m=64MB, public per-server
// salt): each guess costs an attacker ~0.3s single-CPU with GPUs largely
// neutralized, so score<=1 (<1e6 guesses, days of CPU) is the honest
// "too weak" gate. zxcvbn's own crack-time display is never shown: it
// assumes fast hashes and would understate argon2 cost by ~1e9.

import (
	"errors"
	"fmt"
	"io"
	"math"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/ccojocar/zxcvbn-go"

	"github.com/CleveTok3125/V2V/internal/tui"
)

const (
	// weakMaxScore gates the warn+confirm prompt. Scores above pass silent.
	weakMaxScore = 1
	// displayEntropyCap bounds shown bits. Beyond 128 bits is physically
	// uncrackable; larger numbers imply false precision.
	displayEntropyCap = 128.0
	// maxEntryAttempts bounds double-entry rounds (typos + weak rejects).
	maxEntryAttempts = 3
	// reminderMinRunes / reminderMinTokens feed the static reminder line
	// only. They never gate, never warn conditionally.
	reminderMinRunes  = 20
	reminderMinTokens = 4
)

// StrengthReport is the assessed meter for one passphrase.
type StrengthReport struct {
	Score   int
	Entropy float64 // capped at displayEntropyCap, >= 0
	Capped  bool    // raw entropy exceeded displayEntropyCap: display with a "+" suffix
	Label   string  // yếu / trung bình / mạnh / rất mạnh
	Weak    bool    // Score <= weakMaxScore: caller must warn + confirm
	Tokens  int     // unicode letter/digit runs
	Runes   int
}

// countTokens splits on any non-letter, non-digit rune: spaces, dashes,
// dots, emoji and CJK boundaries all separate tokens.
func countTokens(s string) int {
	n := 0
	inTok := false
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsNumber(r) {
			if !inTok {
				n++
				inTok = true
			}
		} else {
			inTok = false
		}
	}
	return n
}

func strengthLabel(score int) string {
	switch {
	case score <= 1:
		return "yếu"
	case score == 2:
		return "trung bình"
	case score == 3:
		return "mạnh"
	default:
		return "rất mạnh"
	}
}

// AssessPassphrase runs the meter. userInputs carries public personal
// context (username, server host) so the score reflects an attacker who
// knows who you are, not a blind brute-forcer.
func AssessPassphrase(passphrase string, userInputs []string) StrengthReport {
	r := zxcvbn.PasswordStrength(passphrase, userInputs)
	e := r.Entropy
	capped := !math.IsNaN(e) && e > displayEntropyCap
	if math.IsNaN(e) || e < 0 {
		e = 0
	}
	if e > displayEntropyCap {
		e = displayEntropyCap
	}
	return StrengthReport{
		Score:   r.Score,
		Entropy: e,
		Capped:  capped,
		Label:   strengthLabel(r.Score),
		Weak:    r.Score <= weakMaxScore,
		Tokens:  countTokens(passphrase),
		Runes:   utf8.RuneCountInString(passphrase),
	}
}

// ReminderLine is the static tip shown at manual entry. Advisory only.
func ReminderLine() string {
	return fmt.Sprintf("💡 Nên dùng câu dài dễ nhớ (≥%d ký tự, ≥%d từ).",
		reminderMinRunes, reminderMinTokens)
}

// WeakWarning is the one-line alert for weak secrets on non-interactive
// paths (file load) and the gate prompt on interactive entry. It carries
// no entropy number: at most 1 bit leaks to local observers. No label
// interpolation — Weak implies label yếu by construction, so naming it
// again would stutter.
func (r StrengthReport) WeakWarning() string {
	return "⚠️ Tripcode yếu — dễ bị đoán từ badge công khai."
}

// readDoubleEntry reads a hidden entry plus confirmation, up to
// maxEntryAttempts rounds. Mismatches restart the round; exhaustion or a
// read error aborts. Piped stdin consumes exactly two lines per round.
func readDoubleEntry(read func() (string, error)) (string, error) {
	var lastErr error
	for attempt := 1; attempt <= maxEntryAttempts; attempt++ {
		if attempt == 1 {
			fmt.Print("🔑 Nhập tripcode mới: ")
		} else {
			fmt.Printf("🔑 Nhập tripcode mới (lần %d/%d): ", attempt, maxEntryAttempts)
		}
		first, err := read()
		fmt.Println()
		if err != nil {
			return "", err
		}
		fmt.Print("🔑 Nhập lại để xác nhận: ")
		second, err := read()
		fmt.Println()
		if err != nil {
			return "", err
		}
		if first != second {
			fmt.Println("❌ Hai lần nhập không khớp, nhập lại.")
			lastErr = errMismatch
			continue
		}
		if strings.TrimSpace(first) == "" {
			fmt.Println("❌ Tripcode trống, nhập lại.")
			lastErr = errEmpty
			continue
		}
		return first, nil
	}
	if lastErr != nil {
		return "", lastErr
	}
	return "", errMismatch
}

var (
	errMismatch = errors.New("không khớp sau 3 lần nhập")
	errEmpty    = errors.New("tripcode trống")
)

// confirmUseWeak asks whether to proceed with a weak secret. Default is
// No: empty input, read errors and anything but an explicit yes abort.
// Parsing delegates to the shared tui core; only this prompt's wording
// stays local.
func confirmUseWeak(r io.Reader) bool {
	fmt.Print("Vẫn dùng tripcode này? (y/N): ")
	return tui.ConfirmPiped(r)
}

// Package strength owns the passphrase-strength policy for the repo:
// zxcvbn scoring mapped to the shared prompt meter. Both the chat
// client (with personal userInputs context) and v2vctl (context-free)
// assess through here, so label bands, the weak threshold and the
// display cap stay identical in both binaries.
package strength

import (
	"math"

	"github.com/ccojocar/zxcvbn-go"

	"github.com/CleveTok3125/V2V/internal/passprompt"
)

// weakMaxScore gates the warn+confirm prompt. Scores above pass silent.
const weakMaxScore = 1

// displayEntropyCap bounds shown bits. Beyond 128 bits is physically
// unsearchable; the meter marks the clamp instead of printing noise.
const displayEntropyCap = 128.0

func label(score int) string {
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

// Assess maps a passphrase to the shared meter. userInputs carries
// public personal context (username, server host); nil when none
// exists (v2vctl). Pure: safe to call per keystroke.
func Assess(passphrase string, userInputs []string) passprompt.Assessment {
	r := zxcvbn.PasswordStrength(passphrase, userInputs)
	e := r.Entropy
	capped := !math.IsNaN(e) && e > displayEntropyCap
	if math.IsNaN(e) || e < 0 {
		e = 0
	}
	if e > displayEntropyCap {
		e = displayEntropyCap
	}
	return passprompt.Assessment{
		Bits:   e,
		Score:  r.Score,
		Capped: capped,
		Label:  label(r.Score),
		Weak:   r.Score <= weakMaxScore,
	}
}

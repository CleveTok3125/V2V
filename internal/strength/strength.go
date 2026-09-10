// Package strength owns the passphrase-strength policy for the repo:
// zxcvbn scoring reported as a plain Report. Both the chat client
// (with personal userInputs context) and v2vctl (context-free)
// assess through here, so label bands, the weak threshold and the
// display cap stay identical in both binaries. Callers adapt Report
// to their own display types; strength never imports UI packages.
package strength

import (
	"math"

	"github.com/ccojocar/zxcvbn-go"
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

// Report is the policy-owned meter snapshot for one input value.
// Bits is capped display entropy (reference only), Score the 0-4 band,
// Label the band name, Weak whether callers must warn + confirm,
// Capped whether raw entropy exceeded the cap.
type Report struct {
	Bits   float64
	Score  int
	Capped bool
	Label  string
	Weak   bool
}

// Assess maps a passphrase to a Report. userInputs carries public
// personal context (username, server host); nil when none exists
// (v2vctl). Pure: safe to call per keystroke.
func Assess(passphrase string, userInputs []string) Report {
	r := zxcvbn.PasswordStrength(passphrase, userInputs)
	e := r.Entropy
	capped := !math.IsNaN(e) && e > displayEntropyCap
	if math.IsNaN(e) || e < 0 {
		e = 0
	}
	if e > displayEntropyCap {
		e = displayEntropyCap
	}
	return Report{
		Bits:   e,
		Score:  r.Score,
		Capped: capped,
		Label:  label(r.Score),
		Weak:   r.Score <= weakMaxScore,
	}
}

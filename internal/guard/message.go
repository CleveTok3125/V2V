package guard

import (
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/CleveTok3125/V2V/internal/filter"
)

// Limits carries the three send-gate numbers ValidateMessageForSend
// needs. Callers map them from their own config type so guard never
// imports a config package.
type Limits struct {
	MaxMessageLength int
	MaxMessageLine   int
	MessageCooldown  time.Duration
}

// ValidateMessageForSend mirrors server MessageCooldown/Length/Line checks for client zero-trust.
func ValidateMessageForSend(text string, last time.Time, lim *Limits, unlimited bool) error {
	if unlimited {
		return filter.ValidateMessage(text)
	}
	if lim != nil {
		if utf8.RuneCountInString(text) > lim.MaxMessageLength {
			return ErrTooLong
		}
		// Line count, not break count: N newlines make N+1 lines.
		if strings.Count(text, "\n")+1 > lim.MaxMessageLine {
			return ErrTooManyLines
		}
		if time.Since(last) < lim.MessageCooldown {
			return ErrTooFast
		}
	}
	return filter.ValidateMessage(text)
}

var (
	ErrTooLong      = errors.New("too long")
	ErrTooManyLines = errors.New("too many lines")
	ErrTooFast      = errors.New("too fast")
)

func ReadLimit(maxMsgLen int) int64 { return int64(maxMsgLen * 3) }

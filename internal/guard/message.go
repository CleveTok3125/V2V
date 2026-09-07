package guard

import (
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/CleveTok3125/V2V/internal/config"
	"github.com/CleveTok3125/V2V/internal/filter"
)

// ValidateMessageForSend mirrors server MessageCooldown/Length/Line checks for client zero-trust.
func ValidateMessageForSend(text string, last time.Time, cfg *config.DynamicConfig, unlimited bool) error {
	if unlimited {
		return filter.ValidateMessage(text)
	}
	if cfg != nil {
		if utf8.RuneCountInString(text) > cfg.MaxMessageLength {
			return ErrTooLong
		}
		// Line count, not break count: N newlines make N+1 lines.
		if strings.Count(text, "\n")+1 > cfg.MaxMessageLine {
			return ErrTooManyLines
		}
		if time.Since(last) < cfg.MessageCooldown {
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

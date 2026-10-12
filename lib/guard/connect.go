package guard

import "time"

// CheckConnectionRate checks ban and cooldown without side effects.
func CheckConnectionRate(now time.Time, rec RateLimitRecord, lastConnect time.Time, cooldown time.Duration) (ok bool, reason string) {
	if IsBanned(rec, now) {
		return false, "banned"
	}
	if !lastConnect.IsZero() && time.Since(lastConnect) < cooldown {
		return false, "cooldown"
	}
	return true, ""
}

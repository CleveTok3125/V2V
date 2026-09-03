package guard

import "time"

type RateLimitRecord struct {
	FailCount  int
	UnlockTime time.Time
}

// NextPenalty advances the record on auth failure.
func NextPenalty(rec RateLimitRecord, now time.Time) RateLimitRecord {
	rec.FailCount++
	if rec.FailCount >= 5 {
		rec.UnlockTime = now.Add(5 * time.Minute)
		rec.FailCount = 0
	}
	return rec
}

func IsBanned(rec RateLimitRecord, now time.Time) bool {
	return !rec.UnlockTime.IsZero() && now.Before(rec.UnlockTime)
}

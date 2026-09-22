package guard

import "time"

// AuthFailTTL bounds how long an in-progress (not yet banned) failure
// counter is retained. Without it, IPs that never reach the ban
// threshold leave a permanent map entry.
const AuthFailTTL = time.Hour

type RateLimitRecord struct {
	FailCount  int
	UnlockTime time.Time
	// LastSeen is the last failure that touched this record. Used to
	// prune in-progress counters that never reach the ban threshold.
	LastSeen time.Time
}

// NextPenalty advances the record on auth failure.
func NextPenalty(rec RateLimitRecord, now time.Time) RateLimitRecord {
	rec.FailCount++
	rec.LastSeen = now
	if rec.FailCount >= 5 {
		rec.UnlockTime = now.Add(5 * time.Minute)
		rec.FailCount = 0
	}
	return rec
}

func IsBanned(rec RateLimitRecord, now time.Time) bool {
	return !rec.UnlockTime.IsZero() && now.Before(rec.UnlockTime)
}

// ShouldPrune reports whether a penalty record is stale: its ban has
// expired, or it has been idle past ttl without progressing toward a
// ban. Records with a zero LastSeen (constructed outside NextPenalty)
// are kept unless their ban expired.
func ShouldPrune(rec RateLimitRecord, now time.Time, ttl time.Duration) bool {
	if !rec.UnlockTime.IsZero() && now.After(rec.UnlockTime) {
		return true
	}
	return !rec.LastSeen.IsZero() && now.Sub(rec.LastSeen) > ttl
}

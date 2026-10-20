package main

import (
	"net/http"
	"sync/atomic"
	"time"

	"github.com/CleveTok3125/V2V/lib/guard"
)

// softCapRatio is the share of MAX_TOTAL_CONNECTIONS where the server
// starts degrading gracefully: gate sooner, charge history double.
// Internal constant like the attack buckets, not operator policy, so a
// deployment keeps booting with no new key to set.
const softCapRatio = 0.8

// degraded reports soft pressure: under attack or near the hard cap.
// Only cheap signals (flag, headcount), never CPU or memory probes, so
// the hot path stays one flag check plus one map size read.
func (s *ChatServer) degraded() bool {
	if s.Attack != nil && s.Attack.IsUnderAttack() {
		return true
	}
	a := Cfg.Abuse.Load()
	if a == nil || a.MaxTotalConnections < 1 {
		return false
	}
	threshold := int(float64(a.MaxTotalConnections) * softCapRatio)
	if threshold < 1 {
		threshold = 1
	}
	return s.clientCount()+int(atomic.LoadInt64(&s.Inflight)) >= threshold
}

// rejectCooldown refuses with 429 and the remaining wait from meter.
// Call after Allow denied: the recorded time supplies Retry-After.
func rejectCooldown(w http.ResponseWriter, meter *guard.CooldownMap, ip string, d time.Duration, msg string) {
	setRetryAfter(w, meter.RetryAfter(ip, d))
	http.Error(w, msg, http.StatusTooManyRequests)
}

// rejectIPBusy refuses per-IP slot exhaustion. Slot count is not time
// based, so retry is a fixed short wait like the global cap.
func rejectIPBusy(w http.ResponseWriter, msg string) {
	setRetryAfter(w, 5*time.Second)
	http.Error(w, msg, http.StatusTooManyRequests)
}

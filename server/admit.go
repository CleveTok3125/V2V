package main

import (
	"net/http"
	"time"

	"github.com/CleveTok3125/V2V/lib/guard"
)

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

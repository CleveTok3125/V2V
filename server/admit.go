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

// RejectStat tallies cumulative admission refusals by cause. Counters
// only; the per-second pressure still lives in the AttackState buckets.
// int64 plus atomic, like Inflight: no new lock on the hot path.
type RejectStat struct {
	Cooldown int64
	PerIP    int64
	Gate     int64
	Full     int64
}

// rejectCause names one RejectStat counter for countReject.
type rejectCause int

const (
	rejectCooldownCause rejectCause = iota
	rejectPerIPCause
	rejectGateCause
	rejectFullCause
)

// countReject tallies one refusal. Helpers call it, so callers only
// choose the helper and the totals stay consistent.
func (s *ChatServer) countReject(cause rejectCause) {
	switch cause {
	case rejectCooldownCause:
		atomic.AddInt64(&s.Rejects.Cooldown, 1)
	case rejectPerIPCause:
		atomic.AddInt64(&s.Rejects.PerIP, 1)
	case rejectGateCause:
		atomic.AddInt64(&s.Rejects.Gate, 1)
	case rejectFullCause:
		atomic.AddInt64(&s.Rejects.Full, 1)
	}
}

// loadRejects snapshots the cumulative refusal counters.
func (s *ChatServer) loadRejects() RejectStat {
	return RejectStat{
		Cooldown: atomic.LoadInt64(&s.Rejects.Cooldown),
		PerIP:    atomic.LoadInt64(&s.Rejects.PerIP),
		Gate:     atomic.LoadInt64(&s.Rejects.Gate),
		Full:     atomic.LoadInt64(&s.Rejects.Full),
	}
}

// rejectCooldown refuses with 429 and the remaining wait from meter.
// Call after Allow denied: the recorded time supplies Retry-After.
func (s *ChatServer) rejectCooldown(w http.ResponseWriter, meter *guard.CooldownMap, clientIP string, window time.Duration, msg string) {
	s.countReject(rejectCooldownCause)
	setRetryAfter(w, meter.RetryAfter(clientIP, window))
	http.Error(w, msg, http.StatusTooManyRequests)
}

// rejectIPBusy refuses per-IP slot exhaustion. Slot count is not time
// based, so retry is a fixed short wait like the global cap.
func (s *ChatServer) rejectIPBusy(w http.ResponseWriter, msg string) {
	s.countReject(rejectPerIPCause)
	setRetryAfter(w, 5*time.Second)
	http.Error(w, msg, http.StatusTooManyRequests)
}

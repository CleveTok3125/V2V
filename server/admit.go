package main

import (
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/CleveTok3125/V2V/internal/serverconfig"
	"github.com/CleveTok3125/V2V/lib/guard"
)

// degraded reports soft pressure: under attack or past the configured
// share of the hard cap (SOFT_CAP_RATIO, default 80%). Only cheap
// signals (flag, headcount), never CPU or memory probes, so the hot
// path stays one flag check plus one map size read.
func (s *ChatServer) degraded() bool {
	if s.Attack != nil && s.Attack.IsUnderAttack() {
		return true
	}
	a := Cfg.Abuse.Load()
	if a == nil || a.MaxTotalConnections < 1 {
		return false
	}
	ratio := serverconfig.DefaultSoftCapRatio
	if a.SoftCapRatio > 0 {
		ratio = a.SoftCapRatio
	}
	threshold := int(float64(a.MaxTotalConnections) * ratio)
	if threshold < 1 {
		threshold = 1
	}
	return s.clientCount()+int(atomic.LoadInt64(&s.Inflight)) >= threshold
}

// RejectStat tallies cumulative admission refusals by cause plus shed
// send-queue drops. Full covers both the pre-upgrade 503 and the
// post-register burst kick: either way the address was turned away for
// capacity. One mutex guards every counter so a snapshot is exact; it
// only fires on refusal/drop paths, never steady state, so contention
// is nil in practice. Deadlock-free by construction, not by convention:
// the methods below take this mutex and touch only its own fields,
// acquiring nothing else, so no lock cycle can ever form through it no
// matter which outer lock the caller holds. Keep it that way: these
// methods must never call out while holding the lock.
//
// Never copy one after use; snapshot it instead. Hub methods reach it
// through Hub.stat, wired in NewChatServer; a nil Hub.stat (a bare Hub
// in tests) drops the count instead of panicking.
type RejectStat struct {
	mu       sync.Mutex
	cooldown int64
	perIP    int64
	gate     int64
	full     int64
	drops    int64
}

// RejectSnapshot is one exact point-in-time reading. Plain data, safe
// to copy and log.
type RejectSnapshot struct {
	Cooldown, PerIP, Gate, Full, Drops int64
}

// rejectCause names one refusal counter for countReject.
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
	s.Rejects.addReject(cause)
}

// addReject tallies one refusal by cause.
func (st *RejectStat) addReject(cause rejectCause) {
	if st == nil {
		return
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	switch cause {
	case rejectCooldownCause:
		st.cooldown++
	case rejectPerIPCause:
		st.perIP++
	case rejectGateCause:
		st.gate++
	case rejectFullCause:
		st.full++
	}
}

// addDrop tallies one frame shed on a full send queue.
func (st *RejectStat) addDrop() {
	if st == nil {
		return
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	st.drops++
}

// loadRejects snapshots every counter at once.
func (s *ChatServer) loadRejects() RejectSnapshot {
	return s.Rejects.snapshot()
}

// snapshot copies every counter under one lock.
func (st *RejectStat) snapshot() RejectSnapshot {
	if st == nil {
		return RejectSnapshot{}
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	return RejectSnapshot{
		Cooldown: st.cooldown,
		PerIP:    st.perIP,
		Gate:     st.gate,
		Full:     st.full,
		Drops:    st.drops,
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

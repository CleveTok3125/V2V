package guard

import (
	"sync"
	"time"
)

// BudgetMap is a per-key token bucket for cost-varying requests: each
// Allow charges cost tokens, refilled at perSec up to burst. It replaces
// a fixed time cooldown for history fetches, where a flat interval either
// throttles cheap requests too hard or lets an expensive one through.
type BudgetMap struct {
	mu     sync.Mutex
	states map[string]*budgetState
	now    func() time.Time // injectable for tests
}

type budgetState struct {
	tokens  float64
	updated time.Time
}

func NewBudgetMap() *BudgetMap {
	return &BudgetMap{states: make(map[string]*budgetState), now: time.Now}
}

// Allow reports whether key may spend cost, charging it when allowed.
// burst and perSec come per call so a config reload takes effect without
// rebuilding the map. A non-positive cost is free; a non-positive burst
// refuses everything (fail closed).
func (b *BudgetMap) Allow(key string, cost, burst, perSec float64) bool {
	if cost <= 0 {
		return true
	}
	if burst <= 0 {
		return false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.now()
	st, ok := b.states[key]
	if !ok {
		st = &budgetState{tokens: burst, updated: now}
		b.states[key] = st
		if len(b.states) > 1000 {
			b.evictLocked(now)
		}
	}
	if perSec > 0 {
		if elapsed := now.Sub(st.updated).Seconds(); elapsed > 0 {
			st.tokens += elapsed * perSec
			if st.tokens > burst {
				st.tokens = burst
			}
			st.updated = now
		}
	} else {
		st.updated = now
	}
	if st.tokens < cost {
		return false
	}
	st.tokens -= cost
	return true
}

func (b *BudgetMap) evictLocked(now time.Time) {
	cutoff := now.Add(-10 * time.Minute)
	for k, st := range b.states {
		if st.updated.Before(cutoff) {
			delete(b.states, k)
		}
	}
}

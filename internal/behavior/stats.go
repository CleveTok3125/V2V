package behavior

import "sync"

// Stats is the calibration histogram: tier counts per window, with no
// per-IP data and no content. The server snapshots it on a timer into
// the stats file so operators can tune weights and thresholds from
// real distributions instead of guesses.
type Stats struct {
	mu    sync.Mutex
	tiers map[int]int
	total int64
}

// NewStats creates an empty histogram.
func NewStats() *Stats {
	return &Stats{tiers: map[int]int{}}
}

// Observe counts one scoring outcome by tier. Negative tiers are
// ignored (defensive: tiers are non-negative by construction).
func (s *Stats) Observe(tier int) {
	if tier < 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tiers[tier]++
	s.total++
}

// Total reports observations since the last reset.
func (s *Stats) Total() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.total
}

// SnapshotAndReset returns the window counts and starts a fresh window.
func (s *Stats) SnapshotAndReset() map[int]int {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.tiers
	s.tiers = map[int]int{}
	s.total = 0
	return out
}

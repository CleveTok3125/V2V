package main

import (
	"sort"
	"time"
)

// Exposed-trip registry bounds. Publishing a seed costs its publisher
// the identity, so arrivals are rare, but minting keys is free: the map
// still needs the same TTL + hard cap treatment as TripChains.
const (
	exposedTripsTTL = 24 * time.Hour
	maxExposedTrips = 10000
)

// exposedEntry remembers when a seed was revealed. Touch-on-use keeps an
// active exposed identity flagged while idle entries age out.
type exposedEntry struct {
	At time.Time
}

// markTripExposed records a published seed at the current time. Only a
// key holder could have produced the announcement, so recording needs
// no further proof.
func (s *ChatServer) markTripExposed(pubHex string) {
	s.markTripExposedAt(pubHex, time.Now())
}

// markTripExposedAt records a published seed at the given timestamp,
// skipping entries that have already aged past the TTL.
func (s *ChatServer) markTripExposedAt(pubHex string, at time.Time) {
	now := time.Now()
	if at.IsZero() {
		at = now
	}
	if now.Sub(at) > exposedTripsTTL {
		return
	}
	s.ExposedTrips.Store(pubHex, exposedEntry{At: at})
	s.pruneExposedTrips(now)
}

// isTripExposed reports whether the pub's seed was revealed. Touches
// active entries so they stay flagged; drops idle ones.
func (s *ChatServer) isTripExposed(pubHex string) bool {
	return s.isTripExposedAt(pubHex, time.Now())
}

func (s *ChatServer) isTripExposedAt(pubHex string, at time.Time) bool {
	v, ok := s.ExposedTrips.Load(pubHex)
	if !ok {
		return false
	}
	e, ok := v.(exposedEntry)
	if !ok {
		s.ExposedTrips.Delete(pubHex)
		return false
	}
	now := time.Now()
	if now.Sub(e.At) > exposedTripsTTL {
		s.ExposedTrips.Delete(pubHex)
		return false
	}
	if at.IsZero() {
		at = now
	}
	if at.After(e.At) && now.Sub(at) <= exposedTripsTTL {
		s.ExposedTrips.Store(pubHex, exposedEntry{At: at})
	}
	return true
}

// pruneExposedTrips drops idle entries and enforces the hard cap,
// evicting the least recently seen first. Mirrors pruneTripChains.
func (s *ChatServer) pruneExposedTrips(now time.Time) {
	type entry struct {
		key string
		at  time.Time
	}
	s.ExposedTripsMu.Lock()
	defer s.ExposedTripsMu.Unlock()
	var kept []entry
	s.ExposedTrips.Range(func(k, v any) bool {
		e, ok := v.(exposedEntry)
		if !ok {
			s.ExposedTrips.Delete(k)
			return true
		}
		if !e.At.IsZero() && now.Sub(e.At) > exposedTripsTTL {
			s.ExposedTrips.Delete(k)
			return true
		}
		key, _ := k.(string)
		kept = append(kept, entry{key: key, at: e.At})
		return true
	})
	if len(kept) <= maxExposedTrips {
		return
	}
	sort.Slice(kept, func(i, j int) bool { return kept[i].at.Before(kept[j].at) })
	for _, e := range kept[:len(kept)-maxExposedTrips] {
		s.ExposedTrips.Delete(e.key)
	}
}

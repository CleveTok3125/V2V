package behavior

import (
	"bufio"
	"encoding/json"
	"errors"
	"os"
	"sync"
	"time"
)

// record is one persisted profile line: key plus profile.
type record struct {
	Key     string   `json:"key"`
	Profile *Profile `json:"profile"`
}

// Store is the in-RAM profile set with JSON-lines persistence. The file
// holds timing/count metadata only, never message content. Retention is
// per tier: suspicious profiles outlive clean ones.
type Store struct {
	mu    sync.Mutex
	items map[string]*Profile
	cap   int
}

// NewStore creates a store capped at cap profiles (0 means uncapped).
func NewStore(cap int) *Store {
	return &Store{items: map[string]*Profile{}, cap: cap}
}

// evictSample bounds the eviction scan: Go randomizes map iteration
// start, so inspecting the first N entries approximates "evict one of
// the stalest" in O(1) instead of O(n) per insert under an IP flood.
const evictSample = 16

// Get returns the profile for key, creating it on first use. Over cap,
// the stalest of a bounded sample is evicted.
func (s *Store) Get(key string) *Profile {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p, ok := s.items[key]; ok {
		return p
	}
	if s.cap > 0 && len(s.items) >= s.cap {
		var oldest string
		var oldestTime time.Time
		first := true
		n := 0
		for k, p := range s.items {
			if first || p.LastSeen.Before(oldestTime) {
				oldest, oldestTime, first = k, p.LastSeen, false
			}
			n++
			if n >= evictSample {
				break
			}
		}
		delete(s.items, oldest)
	}
	p := &Profile{}
	s.items[key] = p
	return p
}

// Len reports the live profile count.
func (s *Store) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.items)
}

// Peek returns an existing profile without creating one. False means
// the key has never been seen.
func (s *Store) Peek(key string) (*Profile, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.items[key]
	return p, ok
}

// Prune drops profiles idle past their tier retention (tiers missing
// from retention fall back to def) and returns the pruned keys so
// callers can clean their own indexes.
func (s *Store) Prune(now time.Time, retentionByTier map[int]time.Duration, def time.Duration) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var pruned []string
	for k, p := range s.items {
		ret, ok := retentionByTier[p.Tier]
		if !ok {
			ret = def
		}
		if now.Sub(p.LastSeen) > ret {
			delete(s.items, k)
			pruned = append(pruned, k)
		}
	}
	return pruned
}

// Save writes every profile as one JSON line, atomically via rename.
func (s *Store) Save(path string) error {
	s.mu.Lock()
	recs := make([]record, 0, len(s.items))
	for k, p := range s.items {
		cp := *p
		recs = append(recs, record{Key: k, Profile: &cp})
	}
	s.mu.Unlock()

	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	w := bufio.NewWriter(f)
	for _, r := range recs {
		line, err := json.Marshal(r)
		if err != nil {
			f.Close()
			os.Remove(tmp)
			return err
		}
		if _, err := w.Write(append(line, '\n')); err != nil {
			f.Close()
			os.Remove(tmp)
			return err
		}
	}
	if err := w.Flush(); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}

// Load replaces the set from a Save file. A missing file is tolerated
// (fresh boot); a corrupt line aborts with an error and leaves the
// previous set untouched.
func (s *Store) Load(path string) error {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	defer f.Close()
	items := map[string]*Profile{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1024*1024), 1024*1024)
	for sc.Scan() {
		var r record
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			return err
		}
		if r.Key == "" || r.Profile == nil {
			continue
		}
		items[r.Key] = r.Profile
	}
	if err := sc.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	s.items = items
	s.mu.Unlock()
	return nil
}

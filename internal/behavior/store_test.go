package behavior

import (
	"path/filepath"
	"testing"
	"time"
)

func TestStoreRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "behavior.jsonl")
	s := NewStore(100)
	now := time.Now()
	a := s.Get("10.0.0.1")
	a.AddMessage(now)
	a.SetIdentity(IdTrip)
	a.Tier = 2
	b := s.Get("10.0.0.2")
	b.AddConnect(now)
	if err := s.Save(path); err != nil {
		t.Fatal(err)
	}
	loaded := NewStore(100)
	if err := loaded.Load(path); err != nil {
		t.Fatal(err)
	}
	if loaded.Len() != 2 {
		t.Fatalf("want 2 profiles, got %d", loaded.Len())
	}
	got := loaded.Get("10.0.0.1")
	if got.TotalMsgs != 1 || got.Identity != IdTrip || got.Tier != 2 {
		t.Fatalf("round trip lost data: %+v", got)
	}
	// Loading twice must not duplicate.
	if err := loaded.Load(path); err != nil {
		t.Fatal(err)
	}
	if loaded.Len() != 2 {
		t.Fatalf("reload must replace, got %d", loaded.Len())
	}
}

func TestStorePruneByTierRetention(t *testing.T) {
	s := NewStore(100)
	old := time.Now().Add(-48 * time.Hour)
	fresh := time.Now()
	low := s.Get("low")
	low.LastSeen = old
	low.Tier = 0
	high := s.Get("high")
	high.LastSeen = old
	high.Tier = 3
	now := s.Get("now")
	now.LastSeen = fresh
	ret := map[int]time.Duration{0: 24 * time.Hour, 3: 72 * time.Hour}
	s.Prune(fresh, ret, time.Hour)
	if s.Len() != 2 {
		t.Fatalf("want low pruned, high+now kept; len=%d", s.Len())
	}
	if _, ok := s.items["low"]; ok {
		t.Fatal("tier-0 profile past retention must go")
	}
}

func TestStoreCapEvictsOldest(t *testing.T) {
	s := NewStore(2)
	base := time.Now()
	s.Get("a").LastSeen = base
	s.Get("b").LastSeen = base.Add(time.Minute)
	s.Get("c").LastSeen = base.Add(2 * time.Minute)
	if s.Len() != 2 {
		t.Fatalf("cap must hold, len=%d", s.Len())
	}
	if _, ok := s.items["a"]; ok {
		t.Fatal("oldest profile must be evicted")
	}
}

func TestStoreLoadMissing(t *testing.T) {
	s := NewStore(10)
	if err := s.Load(filepath.Join(t.TempDir(), "nope.jsonl")); err != nil {
		t.Fatalf("missing file must be tolerated, got %v", err)
	}
}

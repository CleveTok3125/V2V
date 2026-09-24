package behavior

import "testing"

func TestStatsObserveSnapshot(t *testing.T) {
	s := NewStats()
	s.Observe(0)
	s.Observe(2)
	s.Observe(2)
	snap := s.SnapshotAndReset()
	if snap[0] != 1 || snap[2] != 2 || len(snap) != 2 {
		t.Fatalf("got %v", snap)
	}
	if again := s.SnapshotAndReset(); len(again) != 0 {
		t.Fatalf("reset must empty, got %v", again)
	}
}

func TestStatsNegativeTierIgnored(t *testing.T) {
	s := NewStats()
	s.Observe(-1)
	if n := s.Total(); n != 0 {
		t.Fatalf("negative tier must be ignored, total=%d", n)
	}
}

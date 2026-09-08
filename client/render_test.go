package main

import "testing"

func TestParseHistoryBoundary(t *testing.T) {
	// Sync tracking must not depend on the join-display toggle: with -j
	// the old gated block never set inSync, silently disabling the fork
	// check and feeding replay lines to echo matching.
	if b, start := parseHistoryBoundary("| --- Lịch sử chat gần đây ---"); !b || !start {
		t.Errorf("header = (%v,%v), want (true,true)", b, start)
	}
	if b, start := parseHistoryBoundary("| --- Kết thúc lịch sử (32/142) ---"); !b || start {
		t.Errorf("footer = (%v,%v), want (true,false)", b, start)
	}
	if b, _ := parseHistoryBoundary("| 12:00 Alice: hello"); b {
		t.Error("chat line detected as boundary")
	}
	if b, _ := parseHistoryBoundary(""); b {
		t.Error("empty line detected as boundary")
	}
}

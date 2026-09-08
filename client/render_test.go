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

func TestIsJoinLeaveTagFirst(t *testing.T) {
	if !isJoinLeave(WireMessage{Type: "system", SysKind: "join", Text: "unrelated"}) {
		t.Error("tagged join must match regardless of text")
	}
	if !isJoinLeave(WireMessage{Type: "system", SysKind: "leave", Text: "unrelated"}) {
		t.Error("tagged leave must match regardless of text")
	}
	if isJoinLeave(WireMessage{Type: "system", SysKind: "audit", Text: "x đã tham gia"}) {
		t.Error("audit must not match even with join-like text")
	}
	if isJoinLeave(WireMessage{Type: "system", SysKind: "date", Text: "x"}) {
		t.Error("date must not match")
	}
	// Legacy untagged lines fall back to text sniffing.
	if !isJoinLeave(WireMessage{Type: "system", Text: "12:00 [Hệ thống]: a đã tham gia phòng chat!"}) {
		t.Error("untagged join text must match")
	}
	if isJoinLeave(WireMessage{Type: "system", Text: "plain notice"}) {
		t.Error("plain text must not match")
	}
}

func TestIsDateBannerTagFirst(t *testing.T) {
	if !isDateBanner(WireMessage{Type: "system", SysKind: "date", Text: "x"}) {
		t.Error("tagged date must match")
	}
	if isDateBanner(WireMessage{Type: "system", SysKind: "join", Text: "--- Ngày x ---"}) {
		t.Error("join must not match even with date-like text")
	}
	if !isDateBanner(WireMessage{Type: "system", Text: "--- Ngày 01/01/2026 ---"}) {
		t.Error("untagged date text must match")
	}
}

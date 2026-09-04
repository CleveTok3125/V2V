package main

import (
	"strings"
	"testing"
)

func TestClassifyTab(t *testing.T) {
	if got := classifyTab("| 09:01 Alice#ab12: hello"); got != TabChat {
		t.Errorf("chat line -> %d, want TabChat", got)
	}
	if got := classifyTab("  └─ ✍️ \x1b]8;;https://h/api/trip/verify?pub=x\x1b\\◆ ab12\x1b]8;;\x1b\\"); got != TabChat {
		t.Errorf("badge line -> %d, want TabChat", got)
	}
	for _, s := range []string{
		"| 09:01 [Hệ thống]: Bob đã tham gia phòng chat!",
		"| 09:01 [Hệ thống]: Bob đã rời phòng chat.",
		"\x1b[36m--- Ngày 03/09/2026 ---\x1b[0m",
		"| [Hệ thống]: Bạn đang chat quá nhanh! Vui lòng đợi 200ms.",
		"| [Local]: Auto-verify đã BẬT",
	} {
		if got := classifyTab(s); got != TabSystem {
			t.Errorf("system line %q -> %d, want TabSystem", s, got)
		}
	}
	// History boundaries delimit the chat history stream, so they stay
	// on TabChat and never pollute TabSystem.
	for _, s := range []string{
		"--- Lịch sử chat gần đây ---",
		"--- Kết thúc lịch sử ---",
	} {
		if got := classifyTab(s); got != TabChat {
			t.Errorf("boundary line %q -> %d, want TabChat", s, got)
		}
	}
}

func TestTabBufferDualLimit(t *testing.T) {
	b := newTabBuffer(3, 1000000)
	b.append("a")
	b.append("b")
	b.append("c")
	b.append("d")
	if len(b.lines) != 3 || strings.Join(b.lines, "") != "bcd" {
		t.Fatalf("line eviction failed: %q", b.lines)
	}
	b2 := newTabBuffer(1000000, 4)
	b2.append("aa")
	b2.append("bb")
	b2.append("cc")
	if len(b2.lines) != 2 || strings.Join(b2.lines, "") != "bbcc" {
		t.Fatalf("byte eviction failed: %q", b2.lines)
	}
}

func TestTabCapsFromConfig(t *testing.T) {
	cl, cb, sl, sb := tabCaps()
	if cl <= 0 || cb <= 0 || sl <= 0 || sb <= 0 {
		t.Fatalf("caps must be positive: %d %d %d %d", cl, cb, sl, sb)
	}
}

func TestTabBufferSpliceOut(t *testing.T) {
	b := newTabBuffer(100, 1000000)
	for _, l := range []string{"a\n", "b ⏳\n", "c ⏳\n", "d\n"} {
		b.append(l)
	}
	b.spliceOut(1, 3)
	if len(b.lines) != 2 || b.lines[0] != "a\n" || b.lines[1] != "d\n" {
		t.Fatalf("splice failed: %q", b.lines)
	}
	if b.size != len("a\n")+len("d\n") {
		t.Fatalf("size not fixed: %d", b.size)
	}
	b.spliceOut(-5, 99)
	if len(b.lines) != 0 || b.size != 0 {
		t.Fatalf("clamp splice failed: %q %d", b.lines, b.size)
	}
}

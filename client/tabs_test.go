package main

import (
	"testing"
)

func TestTabBufferDualLimit(t *testing.T) {
	b := newTabBuffer(3, 1000000)
	for _, l := range []string{"a", "b", "c", "d"} {
		b.append(noticeEntry(l))
	}
	if len(b.lines) != 3 || entriesText(b.lines) != "b\n\nc\n\nd\n\n" {
		t.Fatalf("line eviction failed: %q", entriesText(b.lines))
	}
	// Each entry is its line plus the blank that separates it, so the byte
	// budget fits two of them and no more.
	b2 := newTabBuffer(1000000, 7)
	for _, l := range []string{"a", "b", "c"} {
		b2.append(noticeEntry(l))
	}
	if len(b2.lines) != 2 || entriesText(b2.lines) != "b\n\nc\n\n" {
		t.Fatalf("byte eviction failed: %q", entriesText(b2.lines))
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
		b.append(noticeEntry(l))
	}
	b.spliceOut(1, 3)
	if len(b.lines) != 2 || b.lines[0].text() != "a\n\n" || b.lines[1].text() != "d\n\n" {
		t.Fatalf("splice failed: %q", entriesText(b.lines))
	}
	if b.size != len("a\n\n")+len("d\n\n") {
		t.Fatalf("size not fixed: %d", b.size)
	}
	b.spliceOut(-5, 99)
	if len(b.lines) != 0 || b.size != 0 {
		t.Fatalf("clamp splice failed: %q %d", entriesText(b.lines), b.size)
	}
}

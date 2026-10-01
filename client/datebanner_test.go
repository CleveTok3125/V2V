package main

import (
	"strings"
	"testing"

	"github.com/CleveTok3125/V2V/internal/wire"
)

// dateBanner builds the wire the server publishes for a new day.
func dateBanner(day, text string) WireMessage {
	return WireMessage{
		Type: "system", Time: "00:00",
		Tags: wire.WithTags(wire.TagDate), SysDate: day, Text: text,
	}
}

// sysLineRenders reports whether a rendered buffer holds the text.
func sysLineRenders(t *testing.T, sess *Session, text string) bool {
	t.Helper()
	for _, line := range sess.Display.TabSys.lines {
		if strings.Contains(line, text) {
			return true
		}
	}
	return false
}

// TestDateBannerDedupsOnSysDate covers the repeat a restarted server
// causes: it re-announces today even though the replay already carried
// today's banner, and the two banners must not both appear.
func TestDateBannerDedupsOnSysDate(t *testing.T) {
	sess := sessionForDispatch(t)
	sess.Chain.RenderCache = newRenderCache(8)

	sess.Display.DisplayMu.Lock()
	sess.emitDateBannerLocked("2026-01-02", "\x1b[36m--- Ngày 02/01/2026 ---\x1b[0m")
	sess.emitDateBannerLocked("2026-01-02", "\x1b[36m--- Ngày 02/01/2026 ---\x1b[0m")
	sess.Display.DisplayMu.Unlock()

	if got := countLines(sess.Display.TabSys.lines, "Ngày"); got != 1 {
		t.Fatalf("same sys_date twice must render once, got %d", got)
	}

	// A new day is a new banner.
	sess.Display.DisplayMu.Lock()
	sess.emitDateBannerLocked("2026-01-03", "\x1b[36m--- Ngày 03/01/2026 ---\x1b[0m")
	sess.Display.DisplayMu.Unlock()

	if got := countLines(sess.Display.TabSys.lines, "Ngày"); got != 2 {
		t.Fatalf("a new sys_date must render, got %d banners", got)
	}
}

// TestDateBannerDedupIgnoresText: the repeat is recognised by sys_date,
// not by the wording. Different text on the same day is still a repeat,
// and identical text on a different day is not. This is what keeps a
// reworded banner from reappearing.
func TestDateBannerDedupIgnoresText(t *testing.T) {
	sess := sessionForDispatch(t)
	sess.Display.DisplayMu.Lock()
	sess.emitDateBannerLocked("2026-01-02", "--- Ngày 02/01/2026 ---")
	sess.emitDateBannerLocked("2026-01-02", "--- Ngày hôm nay ---")
	sess.Display.DisplayMu.Unlock()

	if got := countLines(sess.Display.TabSys.lines, "Ngày"); got != 1 {
		t.Fatalf("same day with different text must render once, got %d", got)
	}

	// Same text, different day: both render.
	sess.Display.DisplayMu.Lock()
	sess.emitDateBannerLocked("2026-01-03", "--- Ngày 02/01/2026 ---")
	sess.Display.DisplayMu.Unlock()

	if got := countLines(sess.Display.TabSys.lines, "Ngày"); got != 2 {
		t.Fatalf("same text on a new day must render again, got %d", got)
	}
}

// TestDateBannerWithoutSysDateAlwaysRenders: with no machine value there
// is nothing to compare, so the banner prints. Deliberately no text
// comparison — the point of sys_date is to remove that dependency.
func TestDateBannerWithoutSysDateAlwaysRenders(t *testing.T) {
	sess := sessionForDispatch(t)
	sess.Display.DisplayMu.Lock()
	sess.emitDateBannerLocked("", "--- Ngày 02/01/2026 ---")
	sess.emitDateBannerLocked("", "--- Ngày 02/01/2026 ---")
	sess.Display.DisplayMu.Unlock()

	if got := countLines(sess.Display.TabSys.lines, "Ngày"); got != 2 {
		t.Fatalf("a banner with no sys_date must print every time, got %d", got)
	}
}

// TestStashedBannerFlushesWithItsDay: the stashed wire keeps its sys_date,
// so a banner held until the replay footer still dedups against a day
// already shown.
func TestStashedBannerFlushesWithItsDay(t *testing.T) {
	sess := sessionForDispatch(t)
	sess.Chain.RenderCache = newRenderCache(8)
	banner := dateBanner("2026-01-02", "--- Ngày 02/01/2026 ---")

	sess.Display.DisplayMu.Lock()
	sess.Display.LastDateBanner = "2026-01-02"
	sess.Pending.PendingDateBannerWire = &banner
	sess.flushDateBannerLocked()
	sess.Display.DisplayMu.Unlock()

	if sysLineRenders(t, sess, "Ngày") {
		t.Fatal("stashed banner for an already-shown day must not render")
	}
	if sess.Pending.PendingDateBannerWire != nil {
		t.Fatal("stash must be cleared either way")
	}
}

func countLines(lines []string, needle string) int {
	n := 0
	for _, line := range lines {
		if strings.Contains(line, needle) {
			n++
		}
	}
	return n
}

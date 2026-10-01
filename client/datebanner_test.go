package main

import (
	"bytes"
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

// countLines counts rendered lines containing needle.
func countLines(lines []string, needle string) int {
	n := 0
	for _, line := range lines {
		if strings.Contains(line, needle) {
			n++
		}
	}
	return n
}

// dateSession returns a session that can render date banners into TabSystem.
func dateSession(t *testing.T) (*Session, *bytes.Buffer) {
	t.Helper()
	sess, out := queueTestSession(t)
	sess.Chain.WireIdx = newWireIndex(8)
	sess.Chain.RenderCache = newRenderCache(8)
	return sess, out
}

// TestDateBannerDedupsOnSysDate covers the repeat a restarted server
// causes: it re-announces today even though the replay already carried
// today's banner, and the two banners must not both appear.
func TestDateBannerDedupsOnSysDate(t *testing.T) {
	sess, out := dateSession(t)
	first, second := dateBanner("2026-01-02", "--- Ngày 02/01/2026 ---"), dateBanner("2026-01-02", "--- Ngày 02/01/2026 ---")

	sess.Display.DisplayMu.Lock()
	sess.Pending.PendingDateBannerWire = &first
	sess.flushDateBannerLocked()
	sess.Pending.PendingDateBannerWire = &second
	sess.flushDateBannerLocked()
	sess.Display.DisplayMu.Unlock()
	sess.flushOutputNow()

	if got := countLines(sess.Display.TabSys.lines, "Ngày"); got != 1 {
		t.Fatalf("same sys_date twice must render once, got %d", got)
	}
	if n := strings.Count(out.String(), "Ngày"); n != 1 {
		t.Fatalf("same sys_date must print once, got %d", n)
	}

	// A new day is a new banner.
	sess.Display.DisplayMu.Lock()
	next := dateBanner("2026-01-03", "--- Ngày 03/01/2026 ---")
	sess.Pending.PendingDateBannerWire = &next
	sess.flushDateBannerLocked()
	sess.Display.DisplayMu.Unlock()
	sess.flushOutputNow()

	if got := countLines(sess.Display.TabSys.lines, "Ngày"); got != 2 {
		t.Fatalf("a new sys_date must render, got %d banners", got)
	}
}

// TestDateBannerDedupIgnoresText: the repeat is recognised by sys_date, not
// by the wording. Different text on the same day is still a repeat, and
// identical text on a different day is not. This is what keeps a reworded
// banner from reappearing.
func TestDateBannerDedupIgnoresText(t *testing.T) {
	sess, _ := dateSession(t)
	sameDay := dateBanner("2026-01-02", "--- Ngày hôm nay ---")
	newDay := dateBanner("2026-01-03", "--- Ngày 02/01/2026 ---")
	first := dateBanner("2026-01-02", "--- Ngày 02/01/2026 ---")

	sess.Display.DisplayMu.Lock()
	sess.Pending.PendingDateBannerWire = &first
	sess.flushDateBannerLocked()
	sess.Pending.PendingDateBannerWire = &sameDay
	sess.flushDateBannerLocked()
	sess.Pending.PendingDateBannerWire = &newDay
	sess.flushDateBannerLocked()
	sess.Display.DisplayMu.Unlock()

	if got := countLines(sess.Display.TabSys.lines, "Ngày"); got != 2 {
		t.Fatalf("same day with different text must render once, new day again: got %d", got)
	}
}

// TestDateBannerWithoutSysDateAlwaysRenders: with no machine value there is
// nothing to compare, so the banner prints every time. Deliberately no text
// comparison — the point of sys_date is to remove that dependency.
func TestDateBannerWithoutSysDateAlwaysRenders(t *testing.T) {
	sess, _ := dateSession(t)
	first := dateBanner("", "--- Ngày 02/01/2026 ---")
	second := dateBanner("", "--- Ngày 02/01/2026 ---")

	sess.Display.DisplayMu.Lock()
	sess.Pending.PendingDateBannerWire = &first
	sess.flushDateBannerLocked()
	sess.Pending.PendingDateBannerWire = &second
	sess.flushDateBannerLocked()
	sess.Display.DisplayMu.Unlock()

	if got := countLines(sess.Display.TabSys.lines, "Ngày"); got != 2 {
		t.Fatalf("a banner with no sys_date must print every time, got %d", got)
	}
}

// TestStashedBannerSkipsAlreadyShownDay: the stash keeps its wire, so a
// banner held until a replay footer still dedups against a day already
// shown, and the stash is cleared either way.
func TestStashedBannerSkipsAlreadyShownDay(t *testing.T) {
	sess, _ := dateSession(t)
	shown, banner := dateBanner("2026-01-02", "--- Ngày 02/01/2026 ---"), dateBanner("2026-01-02", "--- Ngày 02/01/2026 ---")

	sess.Display.DisplayMu.Lock()
	sess.Pending.PendingDateBannerWire = &shown
	sess.flushDateBannerLocked()
	sess.Display.LastDateBanner = "2026-01-02"
	sess.Pending.PendingDateBannerWire = &banner
	sess.flushDateBannerLocked()
	sess.Display.DisplayMu.Unlock()

	if got := countLines(sess.Display.TabSys.lines, "Ngày"); got != 1 {
		t.Fatalf("stashed banner for an already-shown day must not render, got %d", got)
	}
	if sess.Pending.PendingDateBannerWire != nil {
		t.Fatal("stash must be cleared either way")
	}
}

// TestDateBannerGatedByDateSwitch: the date gate covers a date banner, so
// switching it off keeps the line in Tab 2 without printing it live.
func TestDateBannerGatedByDateSwitch(t *testing.T) {
	sess, out := dateSession(t)
	sess.Display.Notify = NotifyState{Muted: map[string]bool{wire.TagDate: true}, PowMinTier: 1}
	banner := dateBanner("2026-01-02", "--- Ngày 02/01/2026 ---")

	sess.Display.DisplayMu.Lock()
	sess.Pending.PendingDateBannerWire = &banner
	sess.flushDateBannerLocked()
	sess.Display.DisplayMu.Unlock()
	sess.flushOutputNow()

	if out.String() != "" {
		t.Fatalf("muted date banner must not print live, got %q", out.String())
	}
	if !strings.Contains(strings.Join(sess.Display.TabSys.lines, ""), "Ngày") {
		t.Fatal("muted date banner must still be buffered in Tab 2")
	}
}

package main

import (
	"strings"
	"testing"
)

func TestNotifyKindForWire(t *testing.T) {
	cases := []struct {
		name string
		wire WireMessage
		want string
	}{
		{"chat", WireMessage{Type: "chat", Text: "hi"}, ""},
		{"join", WireMessage{Type: "system", SysKind: "join", Text: "[Hệ thống]: A đã tham gia"}, NotifyKindJoin},
		{"leave", WireMessage{Type: "system", SysKind: "leave", Text: "[Hệ thống]: A đã rời"}, NotifyKindJoin},
		{"date", WireMessage{Type: "system", SysKind: "date", Text: "--- Ngày 01/01/2026 ---"}, NotifyKindDate},
		{"warn", WireMessage{Type: "system", Text: "[Hệ thống]: chậm thôi"}, NotifyKindSystem},
		{"legacy join by text", WireMessage{Type: "system", Text: "[Hệ thống]: B đã tham gia"}, NotifyKindJoin},
	}
	for _, tc := range cases {
		if got := notifyKindForWire(tc.wire); got != tc.want {
			t.Errorf("%s: notifyKindForWire = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestNotifyKindForLine(t *testing.T) {
	cases := []struct {
		name string
		line string
		want string
	}{
		{"chat", "| 12:00 A: hello", ""},
		{"system", "| [Hệ thống]: chậm thôi", NotifyKindSystem},
		{"join", "| [Hệ thống]: A đã tham gia", NotifyKindJoin},
		{"date", "| --- Ngày 01/01/2026 ---", NotifyKindDate},
	}
	for _, tc := range cases {
		if got := notifyKindForLine(tc.line); got != tc.want {
			t.Errorf("%s: notifyKindForLine = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestNotifyKindAllowedDefaultsAndToggles(t *testing.T) {
	sess := NewSession()
	sess.Display.Notify = NotifyState{Pow: true, PowMinTier: 1, History: true, Join: true, Date: true, System: true}
	for _, kind := range notifyKindNames {
		if !sess.notifyKindAllowed(kind) {
			t.Fatalf("kind %q must default to allowed", kind)
		}
	}
	if !sess.notifyKindAllowed("") {
		t.Fatal("unknown/empty kind must always be allowed")
	}
	if sess.notifySetKind("bogus", false) {
		t.Fatal("unknown kind must not be settable")
	}
	if !sess.notifySetKind(NotifyKindPow, false) {
		t.Fatal("known kind must be settable")
	}
	if sess.notifyKindAllowed(NotifyKindPow) {
		t.Fatal("muted kind must be disallowed")
	}
	sess.notifySetAll(false)
	n := sess.notifySnapshot()
	if n.Pow || n.History || n.Join || n.Date || n.System {
		t.Fatal("all-off must clear every gate")
	}
	sess.notifySetAll(true)
	if !sess.notifyKindAllowed(NotifyKindSystem) {
		t.Fatal("all-on must restore every gate")
	}
}

func TestNotifyPowMinTier(t *testing.T) {
	sess := NewSession()
	sess.Display.Notify = NotifyState{Pow: true, PowMinTier: 3}
	if sess.notifyPowLive(2) {
		t.Fatal("tier below floor must stay quiet")
	}
	if !sess.notifyPowLive(3) {
		t.Fatal("tier at floor must be announced")
	}
	sess.notifySetKind(NotifyKindPow, false)
	if sess.notifyPowLive(5) {
		t.Fatal("pow off must mute every tier")
	}
}

// TestEmitLocalFeedbackKindMutesLiveButBuffers pins the core contract:
// a muted notice never reaches the terminal but still lands in Tab 2.
func TestEmitLocalFeedbackKindMutesLiveButBuffers(t *testing.T) {
	sess, out := queueTestSession(t)
	sess.Display.Notify = NotifyState{Pow: false, History: true, Join: true, Date: true, System: true}

	sess.Display.DisplayMu.Lock()
	sess.emitLocalFeedbackKind(NotifyKindPow, "| [Local]: muted\n")
	sess.Display.DisplayMu.Unlock()
	sess.flushOutputNow()

	if out.String() != "" {
		t.Fatalf("muted notice must not print live, got %q", out.String())
	}
	if !strings.Contains(strings.Join(sess.Display.TabSys.lines, ""), "muted") {
		t.Fatal("muted notice must still be buffered in Tab 2")
	}

	sess.notifySetKind(NotifyKindPow, true)
	sess.Display.DisplayMu.Lock()
	sess.emitLocalFeedbackKind(NotifyKindPow, "| [Local]: loud\n")
	sess.Display.DisplayMu.Unlock()
	sess.flushOutputNow()

	if !strings.Contains(out.String(), "loud") {
		t.Fatalf("unmuted notice must print live, got %q", out.String())
	}
}

// TestCriticalNoticeBypassesGates: emitLocalFeedback (critical warnings,
// command output) is never muted even with every gate off.
func TestCriticalNoticeBypassesGates(t *testing.T) {
	sess, out := queueTestSession(t)
	sess.Display.Notify = NotifyState{Pow: false, History: false, Join: false, Date: false, System: false}

	sess.Display.DisplayMu.Lock()
	sess.emitLocalFeedback("| [Local]: Chuỗi tin bị đứt\n")
	sess.Display.DisplayMu.Unlock()
	sess.flushOutputNow()

	if !strings.Contains(out.String(), "bị đứt") {
		t.Fatalf("critical notice must always print live, got %q", out.String())
	}
}

func TestApplyQuietFlags(t *testing.T) {
	old := CLI.Quiet
	defer func() { CLI.Quiet = old }()

	sess := NewSession()
	sess.Display.Notify = NotifyState{Pow: true, PowMinTier: 1, History: true, Join: true, Date: true, System: true}
	CLI.Quiet = []string{"history", "ALL"}
	sess.applyQuietFlags()

	if sess.notifyKindAllowed(NotifyKindHistory) || sess.notifyKindAllowed(NotifyKindPow) {
		t.Fatal("--quiet names (and all) must mute the named gates")
	}
}

func TestParseOnOff(t *testing.T) {
	for _, v := range []string{"on", "ON", "true"} {
		if got, ok := parseOnOff(v); !ok || !got {
			t.Fatalf("parseOnOff(%q) = %v,%v, want true,true", v, got, ok)
		}
	}
	for _, v := range []string{"off", "OFF", "false"} {
		if got, ok := parseOnOff(v); !ok || got {
			t.Fatalf("parseOnOff(%q) = %v,%v, want false,true", v, got, ok)
		}
	}
	if _, ok := parseOnOff("maybe"); ok {
		t.Fatal("invalid value must be rejected")
	}
}

// TestRenderChatBlockMutedSystemBuffersTabSys exercises the wire path:
// a system wire whose gate is off is buffered but never printed live.
func TestRenderChatBlockMutedSystemBuffersTabSys(t *testing.T) {
	sess, out := queueTestSession(t)
	sess.Chain.WireIdx = newWireIndex(10)
	sess.Chain.RenderCache = newRenderCache(10)
	sess.Display.Notify = NotifyState{Pow: true, PowMinTier: 1, History: true, Join: true, Date: true, System: false}

	sysWire := WireMessage{Type: "system", Text: "[Hệ thống]: rate limit"}

	sess.Display.DisplayMu.Lock()
	sess.renderChatBlock(sysWire)
	sess.Display.DisplayMu.Unlock()
	sess.flushOutputNow()

	if strings.Contains(out.String(), "rate limit") {
		t.Fatalf("muted system wire must not print live, got %q", out.String())
	}
	if !strings.Contains(strings.Join(sess.Display.TabSys.lines, ""), "rate limit") {
		t.Fatal("muted system wire must still be buffered in Tab 2")
	}

	sess.notifySetKind(NotifyKindSystem, true)
	sess.Display.DisplayMu.Lock()
	sess.renderChatBlock(WireMessage{Type: "system", Text: "[Hệ thống]: live now"})
	sess.Display.DisplayMu.Unlock()
	sess.flushOutputNow()

	if !strings.Contains(out.String(), "live now") {
		t.Fatalf("unmuted system wire must print live, got %q", out.String())
	}
}

// TestPowNoticeMutedStillBuffers: a muted PoW notice must still land in
// Tab 2 (only the live print is suppressed), unlike the old early return.
func TestPowNoticeMutedStillBuffers(t *testing.T) {
	sess, out := queueTestSession(t)
	sess.Display.Notify = NotifyState{Pow: false, PowMinTier: 1, History: true, Join: true, Date: true, System: true}

	sess.Display.DisplayMu.Lock()
	sess.emitLocalFeedbackLive(sess.notifyPowLive(1), "| [Local]: Đang giải PoW (tier 1)…\n")
	sess.Display.DisplayMu.Unlock()
	sess.flushOutputNow()

	if out.String() != "" {
		t.Fatalf("muted pow notice must not print live, got %q", out.String())
	}
	if !strings.Contains(strings.Join(sess.Display.TabSys.lines, ""), "Đang giải PoW") {
		t.Fatal("muted pow notice must still be buffered in Tab 2")
	}
}

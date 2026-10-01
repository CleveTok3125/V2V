package main

import (
	"strings"
	"testing"

	"github.com/CleveTok3125/V2V/internal/wire"
)

// TestNotifyTagsForWire: a system line is gated on its tags, chat is never
// gated, and a notice from a peer that predates tagging still falls under
// the system switch instead of escaping it.
func TestNotifyTagsForWire(t *testing.T) {
	cases := []struct {
		name string
		msg  WireMessage
		want string
	}{
		{"chat", WireMessage{Type: "chat", Text: "hi"}, ""},
		{"join", WireMessage{Type: "system", Tags: wire.WithTags(wire.TagJoin)}, wire.TagJoin},
		{"leave", WireMessage{Type: "system", Tags: wire.WithTags(wire.TagLeave)}, wire.TagLeave},
		{"date", WireMessage{Type: "system", Tags: wire.WithTags(wire.TagDate)}, wire.TagDate},
		{"limit", WireMessage{Type: "system", Tags: wire.WithTags(wire.TagLimit)}, wire.TagLimit},
		{"screening", WireMessage{Type: "system", Tags: wire.WithTags(wire.TagPowScreen)}, wire.TagPowScreen},
		// No tag: the root still applies, so the system switch covers it.
		{"untagged", WireMessage{Type: "system", Text: "x"}, wire.TagRoot},
	}
	for _, tc := range cases {
		got := notifyTagsForWire(tc.msg)
		if tc.want == "" {
			if len(got) != 0 {
				t.Errorf("%s: chat must not be gated, got %v", tc.name, got)
			}
			continue
		}
		if !wire.HasTag(got, tc.want) {
			t.Errorf("%s: notifyTagsForWire = %v, want it to carry %q", tc.name, got, tc.want)
		}
	}
}

// TestNotifyTagsAllowedAncestorGating: muting a node hides its whole
// subtree, which is the reason the tags travel as a chain.
func TestNotifyTagsAllowedAncestorGating(t *testing.T) {
	sess := NewSession()
	all := NotifyState{Pow: true, PowMinTier: 1, History: true, Join: true, Date: true, System: true}

	sess.Display.Notify = all
	if !sess.notifyTagsAllowed(wire.WithTags(wire.TagPowScreen)) {
		t.Fatal("all gates on must allow a screening notice")
	}

	// Muting the leaf hides only that leaf.
	sess.Display.Notify = all
	sess.Display.Notify.Pow = false
	if sess.notifyTagsAllowed(wire.WithTags(wire.TagPowScreen)) {
		t.Fatal("muted pow must hide the screening notice")
	}
	if !sess.notifyTagsAllowed(wire.WithTags(wire.TagLimit)) {
		t.Fatal("muting pow must not hide an unrelated notice")
	}

	// The system switch is the root: it hides every notice.
	sess.Display.Notify = all
	sess.Display.Notify.System = false
	for _, tag := range []string{wire.TagJoin, wire.TagDate, wire.TagLimit, wire.TagPowScreen, wire.TagHistoryEnd, wire.TagAuth} {
		if sess.notifyTagsAllowed(wire.WithTags(tag)) {
			t.Errorf("muting the root must hide %q", tag)
		}
	}

	// Untagged content is never muted.
	sess.notifySetAll(false)
	if !sess.notifyTagsAllowed(nil) {
		t.Fatal("a line with no tags must always print")
	}
}

func TestNotifyKindAllowedDefaultsAndToggles(t *testing.T) {
	sess := NewSession()
	sess.Display.Notify = NotifyState{Pow: true, PowMinTier: 1, History: true, Join: true, Date: true, System: true}
	for _, tag := range []string{wire.TagPow, wire.TagHistory, wire.TagJoin, wire.TagDate, wire.TagRoot} {
		if !sess.notifyTagsAllowed(wire.WithTags(tag)) {
			t.Fatalf("tag %q must default to allowed", tag)
		}
	}
	if sess.notifySetKind("bogus", false) {
		t.Fatal("unknown kind must not be settable")
	}
	if !sess.notifySetKind(NotifyKindPow, false) {
		t.Fatal("known kind must be settable")
	}
	if sess.notifyTagsAllowed(wire.WithTags(wire.TagPowScreen)) {
		t.Fatal("muted kind must be disallowed")
	}
	sess.notifySetAll(false)
	n := sess.notifySnapshot()
	if n.Pow || n.History || n.Join || n.Date || n.System {
		t.Fatal("all-off must clear every gate")
	}
	sess.notifySetAll(true)
	if !sess.notifyTagsAllowed(wire.WithTags(wire.TagLimit)) {
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
func TestEmitLocalFeedbackTagsMutesLiveButBuffers(t *testing.T) {
	sess, out := queueTestSession(t)
	sess.Display.Notify = NotifyState{Pow: false, History: true, Join: true, Date: true, System: true}

	sess.Display.DisplayMu.Lock()
	sess.emitLocalFeedbackTags(wire.WithTags(wire.TagPow), "| [Local]: muted\n")
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
	sess.emitLocalFeedbackTags(wire.WithTags(wire.TagPow), "| [Local]: loud\n")
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

	if sess.notifyTagsAllowed(wire.WithTags(wire.TagHistory)) || sess.notifyTagsAllowed(wire.WithTags(wire.TagPow)) {
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

	sysWire := WireMessage{Type: "system", Tags: wire.WithTags(wire.TagLimit), Text: "[Hệ thống]: rate limit"}

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
	sess.renderChatBlock(WireMessage{Type: "system", Tags: wire.WithTags(wire.TagLimit), Text: "[Hệ thống]: live now"})
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

// TestRawFrameIsNotClassifiedByWording: a frame that is not a wire used to
// be routed and gated by matching its text, which put any line containing
// "[Hệ thống]:" into the system tab and under the system gate. Nothing
// marks a raw frame's meaning, so it is now printed ungated in the chat tab
// rather than guessed at.
func TestRawFrameIsNotClassifiedByWording(t *testing.T) {
	sess, out := queueTestSession(t)
	sess.Display.Notify = NotifyState{Pow: true, PowMinTier: 1, History: true, Join: true, Date: true, System: false}
	sess.Chain.WireIdx = newWireIndex(10)
	sess.Chain.RenderCache = newRenderCache(10)

	sess.Display.DisplayMu.Lock()
	sess.emitRawLineLocked("| [Hệ thống]: Bạn đang chat quá nhanh!")
	sess.Display.DisplayMu.Unlock()
	sess.flushOutputNow()

	if !strings.Contains(out.String(), "quá nhanh") {
		t.Fatalf("a raw frame must still print, got %q", out.String())
	}
	if strings.Contains(strings.Join(sess.Display.TabSys.lines, ""), "quá nhanh") {
		t.Fatal("a raw frame must not be routed to the system tab by its wording")
	}
}

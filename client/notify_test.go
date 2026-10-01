package main

import (
	"strings"
	"testing"

	"github.com/CleveTok3125/V2V/internal/config"
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
	all := NotifyState{Muted: map[string]bool{}, PowMinTier: 1}

	sess.Display.Notify = all
	if !sess.notifyTagsAllowed(wire.WithTags(wire.TagPowScreen)) {
		t.Fatal("all gates on must allow a screening notice")
	}

	// Muting a node hides its whole subtree.
	sess.Display.Notify = all
	sess.Display.Notify.Muted[wire.TagPow] = true
	for _, tag := range []string{wire.TagPow, wire.TagPowGate, wire.TagPowScreen} {
		if sess.notifyTagsAllowed(wire.WithTags(tag)) {
			t.Errorf("muting system.pow must hide %q", tag)
		}
	}
	if !sess.notifyTagsAllowed(wire.WithTags(wire.TagLimit)) {
		t.Fatal("muting pow must not hide an unrelated notice")
	}

	// Muting the root hides every notice.
	sess.Display.Notify = NotifyState{Muted: map[string]bool{wire.TagRoot: true}, PowMinTier: 1}
	for _, tag := range []string{wire.TagJoin, wire.TagDate, wire.TagLimit, wire.TagPowScreen, wire.TagHistoryEnd, wire.TagAuth} {
		if sess.notifyTagsAllowed(wire.WithTags(tag)) {
			t.Errorf("muting the root must hide %q", tag)
		}
	}
	// ...and a child of a muted parent reads as off even though its own
	// key was never set, which is what /notify reports per row.
	for _, tag := range wire.AllTags() {
		if tag != wire.TagRoot && sess.notifyTagEnabled(tag) {
			t.Errorf("child %q of a muted root must read as off", tag)
		}
	}

	// Untagged content is never muted.
	sess.notifySetAll(false)
	if !sess.notifyTagsAllowed(nil) {
		t.Fatal("a line with no tags must always print")
	}
}

func TestNotifyTagToggles(t *testing.T) {
	sess := NewSession()
	sess.Display.Notify = NotifyState{Muted: map[string]bool{}, PowMinTier: 1}
	for _, tag := range wire.AllTags() {
		if !sess.notifyTagsAllowed(wire.WithTags(tag)) {
			t.Fatalf("tag %q must default to allowed", tag)
		}
	}
	// A path outside the taxonomy cannot be gated: a switch that no notice
	// would ever match is a lie in the config.
	for _, bad := range []string{"bogus", "system.bogus", "", "join"} {
		if sess.notifySetTag(bad, false) {
			t.Errorf("%q must not be settable", bad)
		}
	}
	if !sess.notifySetTag(wire.TagPow, false) {
		t.Fatal("a tag path must be settable")
	}
	if sess.notifyTagsAllowed(wire.WithTags(wire.TagPowScreen)) {
		t.Fatal("muted node must be disallowed")
	}
	if !sess.notifySetTag(wire.TagPow, true) {
		t.Fatal("unmuting must be settable")
	}
	if !sess.notifyTagsAllowed(wire.WithTags(wire.TagPowScreen)) {
		t.Fatal("unmuting must restore the subtree")
	}
	sess.notifySetAll(false)
	if got := len(sess.Display.Notify.Muted); got != len(wire.AllTags()) {
		t.Fatalf("all-off must mute every tag, muted %d of %d", got, len(wire.AllTags()))
	}
	sess.notifySetAll(true)
	if got := len(sess.Display.Notify.Muted); got != 0 {
		t.Fatalf("all-on must clear the muted set, got %d", got)
	}
}

func TestNotifyPowMinTier(t *testing.T) {
	sess := NewSession()
	sess.Display.Notify = NotifyState{Muted: map[string]bool{}, PowMinTier: 3}
	if sess.notifyPowLive(2) {
		t.Fatal("tier below floor must stay quiet")
	}
	if !sess.notifyPowLive(3) {
		t.Fatal("tier at floor must be announced")
	}
	sess.notifySetTag(wire.TagPow, false)
	if sess.notifyPowLive(5) {
		t.Fatal("muting system.pow must mute every tier")
	}
}

// TestEmitLocalFeedbackKindMutesLiveButBuffers pins the core contract:
// a muted notice never reaches the terminal but still lands in Tab 2.
func TestEmitLocalFeedbackTagsMutesLiveButBuffers(t *testing.T) {
	sess, out := queueTestSession(t)
	sess.Display.Notify = NotifyState{Muted: map[string]bool{wire.TagPow: true}}

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

	sess.notifySetTag(wire.TagPow, true)
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
	sess.Display.Notify = NotifyState{Muted: map[string]bool{wire.TagPow: true, wire.TagHistory: true, wire.TagJoin: true, wire.TagDate: true, wire.TagRoot: true}}

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
	sess.Display.Notify = NotifyState{Muted: map[string]bool{}, PowMinTier: 1}
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
	sess.Display.Notify = NotifyState{Muted: map[string]bool{wire.TagRoot: true}, PowMinTier: 1}

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

	sess.notifySetTag(wire.TagRoot, true)
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
	sess.Display.Notify = NotifyState{Muted: map[string]bool{wire.TagPow: true}, PowMinTier: 1}

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
	sess.Display.Notify = NotifyState{Muted: map[string]bool{wire.TagRoot: true}, PowMinTier: 1}
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

// TestNotifyPowLiveRespectsMutedParent: the tier floor is not the only
// gate on the PoW notice, and the subtree test has to walk ancestors. A
// direct lookup of the system.pow key would leave this line printing after
// the root was muted, which is exactly what the user asked it not to do.
func TestNotifyPowLiveRespectsMutedParent(t *testing.T) {
	sess := NewSession()
	sess.Display.Notify = NotifyState{Muted: map[string]bool{wire.TagRoot: true}, PowMinTier: 1}
	if sess.notifyPowLive(5) {
		t.Fatal("a muted root must suppress the pow notice")
	}
	if sess.notifyTagEnabled(wire.TagPow) {
		t.Fatal("the /notify listing must agree that system.pow is off")
	}
	// Muting an unrelated branch leaves it alone.
	sess.Display.Notify = NotifyState{Muted: map[string]bool{wire.TagHistory: true}, PowMinTier: 1}
	if !sess.notifyPowLive(5) {
		t.Fatal("muting history must not suppress the pow notice")
	}
}

// TestNotifyPowWantedUsesConfigGates: the pre-connect join gate runs before
// there is a session, so it reads the config. It has to apply the same
// subtree rule, or muting the root in ui.notify would leave the gate flow
// printing a notice the user asked not to see.
func TestNotifyPowWantedUsesConfigGates(t *testing.T) {
	old := ClientCfg
	t.Cleanup(func() { ClientCfg = old })

	ClientCfg = config.DefaultClientConfig()
	if !ClientCfg.NotifyTag("system") {
		t.Fatal("test needs the default gates")
	}
	if !notifyPowWanted(1) {
		t.Fatal("default config must announce the gate notice")
	}
	// A tier below the floor stays quiet.
	tier := 3
	ClientCfg.UI.PowMinTier = &tier
	if notifyPowWanted(2) {
		t.Fatal("a tier below the floor must stay quiet")
	}
	if !notifyPowWanted(3) {
		t.Fatal("a tier at the floor must be announced")
	}
	// Muting the pow subtree silences it.
	ClientCfg.UI.Notify = map[string]bool{"system.pow": false}
	if notifyPowWanted(3) {
		t.Fatal("a muted system.pow must silence the gate notice")
	}
	// So does muting the root.
	ClientCfg.UI.Notify = map[string]bool{"system": false}
	if notifyPowWanted(3) {
		t.Fatal("a muted root must silence the gate notice")
	}
	// An unrelated branch leaves it alone.
	ClientCfg.UI.Notify = map[string]bool{"system.history": false}
	if !notifyPowWanted(3) {
		t.Fatal("muting history must not silence the gate notice")
	}
	// No config at all must not silence anything.
	ClientCfg = nil
	if !notifyPowWanted(1) {
		t.Fatal("a nil config must announce the gate notice")
	}
}

// TestNotifyPowWantedHonoursQuietFlag: --quiet must reach the pre-connect
// gate notice as well as the in-chat one. It is parsed before the dial, so
// it is the one gate both flows can see; leaving it out made the same switch
// behave differently for the same notice depending on when it fired.
func TestNotifyPowWantedHonoursQuietFlag(t *testing.T) {
	oldCfg, oldQuiet := ClientCfg, CLI.Quiet
	t.Cleanup(func() { ClientCfg, CLI.Quiet = oldCfg, oldQuiet })
	ClientCfg = config.DefaultClientConfig()

	CLI.Quiet = nil
	if !notifyPowWanted(1) {
		t.Fatal("no --quiet must announce the gate notice")
	}
	CLI.Quiet = []string{"system.pow"}
	if notifyPowWanted(1) {
		t.Fatal("--quiet system.pow must silence the gate notice")
	}
	CLI.Quiet = []string{"all"}
	if notifyPowWanted(1) {
		t.Fatal("--quiet all must silence the gate notice")
	}
	CLI.Quiet = []string{"system"}
	if notifyPowWanted(1) {
		t.Fatal("--quiet system must silence the gate notice")
	}
	CLI.Quiet = []string{"system.history"}
	if !notifyPowWanted(1) {
		t.Fatal("--quiet on an unrelated branch must leave the gate notice")
	}
	// An unknown name is ignored, so a flag written for a newer build does
	// not stop the ones this build understands.
	CLI.Quiet = []string{"system.pow", "not-a-tag"}
	if notifyPowWanted(1) {
		t.Fatal("--quiet system.pow must still apply alongside an unknown name")
	}
}

// TestPowMinTierScope pins the one asymmetry in the PoW tier floor. There
// are two "solving PoW" notices: the pre-connect join gate, printed by a
// free function during dial, and the in-chat challenge, printed by the
// session. The floor in ui.powMinTier gates both. A floor set at runtime
// with /notify can only gate the in-chat one, because the gate notice is
// emitted by the very connection that has not created a session yet — so
// there is no runtime state to read. That is structural, not an oversight,
// and it is why /notify says which notice it governs.
func TestPowMinTierScope(t *testing.T) {
	oldCfg, oldQuiet := ClientCfg, CLI.Quiet
	t.Cleanup(func() { ClientCfg, CLI.Quiet = oldCfg, oldQuiet })
	CLI.Quiet = nil

	t.Run("config floor gates both", func(t *testing.T) {
		ClientCfg = config.DefaultClientConfig()
		floor := 3
		ClientCfg.UI.PowMinTier = &floor
		sess := NewSession()
		sess.Display.Notify = NotifyState{Muted: map[string]bool{}, PowMinTier: ClientCfg.NotifyPowMinTier()}

		for _, tier := range []int{1, 2, 3} {
			if sess.notifyPowLive(tier) != (tier >= floor) {
				t.Errorf("in-chat tier %d: live=%v, want %v", tier, sess.notifyPowLive(tier), tier >= floor)
			}
			if notifyPowWanted(tier) != (tier >= floor) {
				t.Errorf("gate tier %d: live=%v, want %v", tier, notifyPowWanted(tier), tier >= floor)
			}
		}
	})

	t.Run("runtime floor gates the in-chat notice only", func(t *testing.T) {
		ClientCfg = config.DefaultClientConfig() // floor 1
		sess := NewSession()
		sess.Display.Notify = NotifyState{Muted: map[string]bool{}, PowMinTier: 1}
		sess.notifySetPowMinTier(3)

		if sess.notifyPowLive(1) || sess.notifyPowLive(2) {
			t.Error("a runtime floor must hide the in-chat notice below it")
		}
		if !sess.notifyPowLive(3) {
			t.Error("a runtime floor must keep the in-chat notice at it")
		}
		// The gate notice was already printed by this point, so it keeps
		// following the config.
		if !notifyPowWanted(1) {
			t.Error("the gate notice follows ui.powMinTier, not a runtime change")
		}
	})
}

// TestCmdNotifyReportsPowMinScope: /notify has to say which notice a tier
// floor governs. The gate notice is printed by the connection that has not
// built a session yet, so a floor set here cannot reach it; without the
// note, "/notify powmin 3" reads as a promise it does not keep.
func TestCmdNotifyReportsPowMinScope(t *testing.T) {
	sess, out := queueTestSession(t)
	sess.Display.Notify = NotifyState{Muted: map[string]bool{}, PowMinTier: 1}

	// cmdNotify takes DisplayMu itself.
	if !sess.cmdNotify("/notify powmin 3") {
		t.Fatal("/notify powmin should be handled")
	}
	sess.flushOutputNow()
	if !strings.Contains(out.String(), "trong phòng") {
		t.Errorf("the confirmation must scope the change to the in-chat notice: %q", out.String())
	}

	out.Reset()
	if !sess.cmdNotify("/notify") {
		t.Fatal("/notify with no argument should list the gates")
	}
	sess.flushOutputNow()
	text := out.String()
	if !strings.Contains(text, "powmin = 3") {
		t.Errorf("the listing must report the floor just set: %q", text)
	}
	if !strings.Contains(text, "ui.powMinTier") {
		t.Errorf("the listing must say the gate notice follows the config: %q", text)
	}
}

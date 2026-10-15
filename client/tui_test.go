package main

import (
	"io"
	"strings"
	"testing"

	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/CleveTok3125/V2V/lib/config"
	"github.com/CleveTok3125/V2V/lib/wire"
)

func newTestApp() (*app, *Session) {
	historyFile = ""
	sess := NewSession()
	sess.Display.TabChat = newTabBuffer(100, 100000)
	sess.Display.TabSys = newTabBuffer(100, 100000)
	sess.Display.ActiveTab = TabChat
	a := newApp(sess)
	sess.Display.Term = a.term
	sess.Display.Sink = a.sink
	a.width, a.height = 80, 24
	a.layout()
	return a, sess
}

func key(s string) tea.KeyPressMsg {
	if len(s) == 1 {
		return tea.KeyPressMsg{Code: rune(s[0]), Text: s}
	}
	switch s {
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "tab":
		return tea.KeyPressMsg{Code: tea.KeyTab}
	case "shift+tab":
		return tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift}
	case "ctrl+c":
		return tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}
	case "ctrl+d":
		return tea.KeyPressMsg{Code: 'd', Mod: tea.ModCtrl}
	case "alt+enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModAlt}
	case "ctrl+j":
		return tea.KeyPressMsg{Code: 'j', Mod: tea.ModCtrl}
	case "shift+enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModShift}
	case "up":
		return tea.KeyPressMsg{Code: tea.KeyUp}
	case "down":
		return tea.KeyPressMsg{Code: tea.KeyDown}
	case "pgup":
		return tea.KeyPressMsg{Code: tea.KeyPgUp}
	case "pgdown":
		return tea.KeyPressMsg{Code: tea.KeyPgDown}
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	case "backspace":
		return tea.KeyPressMsg{Code: tea.KeyBackspace}
	case "f1":
		return tea.KeyPressMsg{Code: tea.KeyF1}
	}
	panic("unknown key " + s)
}

func TestTuiSinkCoalesces(t *testing.T) {
	k := &tuiSink{}
	k.Notify()
	if k.pending {
		t.Fatal("notify before start must be dropped")
	}
	k.started.Store(true)
	k.Notify()
	if !k.pending {
		t.Fatal("first notify must arm the in-flight flag")
	}
	k.Notify()
	if !k.pending {
		t.Fatal("second notify must fold into the in-flight one")
	}
	k.ack()
	if k.pending {
		t.Fatal("ack must clear the in-flight flag")
	}
	k.Notify()
	if !k.pending {
		t.Fatal("notify after ack must arm again")
	}
}

func TestTuiTermReadLineAndClose(t *testing.T) {
	lines := make(chan string, 1)
	tr := &tuiTerm{lines: lines, sink: &tuiSink{}, closed: make(chan struct{})}
	lines <- "hello"
	got, err := tr.ReadLine()
	if err != nil || got != "hello" {
		t.Fatalf("ReadLine = %q, %v", got, err)
	}
	tr.Close()
	tr.Close() // idempotent
	if _, err := tr.ReadLine(); err != io.EOF {
		t.Fatalf("ReadLine after close = %v, want EOF", err)
	}
}

func TestAppSubmitSendsAndClears(t *testing.T) {
	a, _ := newTestApp()
	a.input.SetValue("xin chào")
	if cmd := a.submit(); cmd != nil {
		t.Fatal("submit must not return a command")
	}
	select {
	case got := <-a.lines:
		if got != "xin chào" {
			t.Fatalf("submitted %q", got)
		}
	default:
		t.Fatal("submit must hand the draft to the session")
	}
	if a.input.Value() != "" {
		t.Fatalf("submit must clear the draft, got %q", a.input.Value())
	}
}

func TestAppEmptySubmitIsIgnored(t *testing.T) {
	a, _ := newTestApp()
	a.input.SetValue("   ")
	a.submit()
	select {
	case got := <-a.lines:
		t.Fatalf("blank draft must not submit, got %q", got)
	default:
	}
}

func TestAppEnterSubmits(t *testing.T) {
	a, _ := newTestApp()
	a.input.SetValue("hi")
	a.Update(key("enter"))
	select {
	case got := <-a.lines:
		if got != "hi" {
			t.Fatalf("enter submitted %q", got)
		}
	default:
		t.Fatal("enter must submit the draft")
	}
}

func TestAppTabSwitchesAndSyncsSession(t *testing.T) {
	a, sess := newTestApp()
	a.Update(key("tab"))
	if a.active != tabIndexSystem || sess.Display.ActiveTab != TabSystem {
		t.Fatalf("tab: app=%d session=%d", a.active, sess.Display.ActiveTab)
	}
	a.Update(key("shift+tab"))
	if a.active != tabIndexChat || sess.Display.ActiveTab != TabChat {
		t.Fatalf("shift+tab: app=%d session=%d", a.active, sess.Display.ActiveTab)
	}
}

func TestAppCtrlCClearsDraft(t *testing.T) {
	a, _ := newTestApp()
	a.input.SetValue("/tab")
	a.Update(key("ctrl+c"))
	if a.input.Value() != "" {
		t.Fatalf("ctrl+c must clear the draft, got %q", a.input.Value())
	}
}

func TestAppCtrlDQuits(t *testing.T) {
	a, _ := newTestApp()
	_, cmd := a.Update(key("ctrl+d"))
	if !a.quitting || cmd == nil {
		t.Fatalf("ctrl+d: quitting=%v cmd=%v", a.quitting, cmd)
	}
}

func TestAppNewlineFallbacks(t *testing.T) {
	a, _ := newTestApp()
	a.input.SetValue("a")
	a.Update(key("alt+enter"))
	if a.input.Value() != "a\n" {
		t.Fatalf("alt+enter must insert a newline, got %q", a.input.Value())
	}
	a.input.SetValue("b")
	a.Update(key("ctrl+j"))
	if a.input.Value() != "b\n" {
		t.Fatalf("ctrl+j must insert a newline, got %q", a.input.Value())
	}
}

func TestAppShiftEnterNeedsEnhancement(t *testing.T) {
	a, _ := newTestApp()
	a.input.SetValue("a")
	a.Update(key("shift+enter"))
	if a.input.Value() != "a" {
		t.Fatalf("without enhancement shift+enter must not insert, got %q", a.input.Value())
	}
	a.enhanced = true
	a.Update(key("shift+enter"))
	if a.input.Value() != "a\n" {
		t.Fatalf("with enhancement shift+enter must insert a newline, got %q", a.input.Value())
	}
}

func TestAppChatTabIsFullStream(t *testing.T) {
	_, sess := newTestApp()
	sess.Display.DisplayMu.Lock()
	sess.emitLocalFeedback("| [Local]: hello\n")
	sess.Display.DisplayMu.Unlock()
	if len(sess.Display.TabChat.lines) != 1 || len(sess.Display.TabSys.lines) != 1 {
		t.Fatalf("system line must land in both tabs: chat=%d sys=%d",
			len(sess.Display.TabChat.lines), len(sess.Display.TabSys.lines))
	}
}

func TestAppRefreshRendersActiveTab(t *testing.T) {
	a, sess := newTestApp()
	sess.Display.TabChat.append(noticeEntry("chat-only"))
	sess.Display.TabSys.append(noticeEntry("sys-only"))
	a.dirty = true
	a.refreshContent()
	view := a.viewport.View()
	if !strings.Contains(view, "chat-only") || strings.Contains(view, "sys-only") {
		t.Fatalf("chat tab view:\n%s", view)
	}
	sess.Display.ActiveTab = TabSystem
	a.dirty = true
	a.refreshContent()
	view = a.viewport.View()
	if !strings.Contains(view, "sys-only") || strings.Contains(view, "chat-only") {
		t.Fatalf("system tab view:\n%s", view)
	}
}

func TestAppHistoryNav(t *testing.T) {
	old := ClientCfg
	ClientCfg = config.DefaultClientConfig()
	on := true
	ClientCfg.UI.History.SaveMessages = &on
	defer func() { ClientCfg = old }()

	a, _ := newTestApp()
	a.hist.add("first")
	a.hist.add("second")
	a.input.SetValue("draft")
	a.Update(key("up"))
	if a.input.Value() != "second" {
		t.Fatalf("up 1 = %q", a.input.Value())
	}
	a.Update(key("up"))
	if a.input.Value() != "first" {
		t.Fatalf("up 2 = %q", a.input.Value())
	}
	a.Update(key("down"))
	if a.input.Value() != "second" {
		t.Fatalf("down 1 = %q", a.input.Value())
	}
	a.Update(key("down"))
	if a.input.Value() != "draft" {
		t.Fatalf("down must restore the draft, got %q", a.input.Value())
	}
}

func TestAppHistoryNavSkipsMultiline(t *testing.T) {
	old := ClientCfg
	ClientCfg = config.DefaultClientConfig()
	on := true
	ClientCfg.UI.History.SaveMessages = &on
	defer func() { ClientCfg = old }()

	a, _ := newTestApp()
	a.hist.add("first")
	a.input.SetValue("line1\nline2")
	a.Update(key("up"))
	if a.input.Value() != "line1\nline2" {
		t.Fatalf("a multi-line draft must keep the arrows, got %q", a.input.Value())
	}
}

func TestAppHelpToggleAndModal(t *testing.T) {
	a, _ := newTestApp()
	a.Update(key("?"))
	if !a.showHelp {
		t.Fatal("? on an empty draft must open help")
	}
	a.Update(key("x"))
	if a.input.Value() != "" {
		t.Fatalf("help must swallow typing, got %q", a.input.Value())
	}
	a.Update(key("esc"))
	if a.showHelp {
		t.Fatal("esc must close help")
	}
	a.input.SetValue("q")
	a.Update(key("?"))
	if a.showHelp {
		t.Fatal("? with a non-empty draft must type, not open help")
	}
	a.Update(key("f1"))
	if !a.showHelp {
		t.Fatal("f1 must open help")
	}
	a.Update(key("enter"))
	if a.showHelp {
		t.Fatal("enter must close help")
	}
}

func TestAppOlderExhaustedStopsPaging(t *testing.T) {
	a, sess := newTestApp()
	sess.Conn = &stubConn{}
	sess.Chain.WireIdx = newWireIndex(16)
	sess.Chain.WireIdx.put(WireMessage{ChainHeight: 7})
	sess.Display.DisplayMu.Lock()
	sess.trackReplayWindow([]string{wire.TagHistory, wire.TagHistoryOlder}, true)
	if !sess.Chain.InOlder {
		sess.Display.DisplayMu.Unlock()
		t.Fatal("an older header must mark the window in flight")
	}
	sess.trackReplayWindow([]string{wire.TagHistory, wire.TagHistoryOlder, wire.TagHistoryExhausted}, false)
	sess.Display.DisplayMu.Unlock()
	if sess.Chain.InOlder || !sess.Chain.OlderExhausted {
		t.Fatalf("exhausted footer: InOlder=%v OlderExhausted=%v", sess.Chain.InOlder, sess.Chain.OlderExhausted)
	}
	if sess.olderAvailable() {
		t.Fatal("older must not be available once exhausted")
	}
	a.atTopArmed = true
	a.maybeLoadOlder()
	if a.pendingOlder {
		t.Fatal("must not request older after exhaustion")
	}
}

func TestOlderWindowPrepends(t *testing.T) {
	_, sess := newTestApp()
	sess.Display.DisplayMu.Lock()
	sess.emitTabLive(true, TabChat, noticeEntry("load notice"))
	sess.emitTabLive(true, TabChat, entry{header: "10:00 A", height: 5})
	sess.trackReplayWindow([]string{wire.TagHistory, wire.TagHistoryOlder}, true)
	sess.emitTabLive(true, TabChat, noticeEntry("--- Lịch sử cũ ---"))
	sess.emitTabLive(true, TabChat, entry{header: "09:00 B", height: 3})
	sess.trackReplayWindow([]string{wire.TagHistory, wire.TagHistoryOlder, wire.TagHistoryEnd}, false)
	lines := append([]entry{}, sess.Display.TabChat.lines...)
	sess.Display.DisplayMu.Unlock()
	got := make([]string, len(lines))
	for i, e := range lines {
		got[i] = e.header
	}
	want := []string{"load notice", "--- Lịch sử cũ ---", "09:00 B", "10:00 A"}
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("order = %q, want %q", got, want)
	}
}

func TestOlderWindowAnchorsAboveBanner(t *testing.T) {
	_, sess := newTestApp()
	sess.Display.DisplayMu.Lock()
	sess.emitTabLive(true, TabChat, noticeEntry("load notice"))
	sess.emitTabLive(true, TabChat, entry{header: "--- Ngày 11/10/2026 ---", banner: true, day: "2026-10-11"})
	sess.emitTabLive(true, TabChat, entry{header: "10:00 A", height: 5})
	sess.trackReplayWindow([]string{wire.TagHistory, wire.TagHistoryOlder}, true)
	sess.emitTabLive(true, TabChat, entry{header: "09:00 B", height: 3})
	sess.trackReplayWindow([]string{wire.TagHistory, wire.TagHistoryOlder, wire.TagHistoryEnd}, false)
	lines := append([]entry{}, sess.Display.TabChat.lines...)
	sess.Display.DisplayMu.Unlock()
	got := make([]string, len(lines))
	for i, e := range lines {
		got[i] = e.header
	}
	want := []string{"load notice", "09:00 B", "--- Ngày 11/10/2026 ---", "10:00 A"}
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("order = %q, want %q", got, want)
	}
}

func TestChatContentDedupesDateBanners(t *testing.T) {
	a, _ := newTestApp()
	lines := []entry{
		{header: "| --- Ngày 11/10/2026 ---", banner: true, day: "2026-10-11"},
		{header: "| 09:00 B", height: 3},
		{header: "| --- Ngày 11/10/2026 ---", banner: true, day: "2026-10-11"},
		{header: "| 10:00 A", height: 5},
		{header: "| --- Ngày 12/10/2026 ---", banner: true, day: "2026-10-12"},
	}
	got := stripANSIForFind(a.chatContent(lines, true))
	if n := strings.Count(got, "Ngày 11/10/2026"); n != 1 {
		t.Fatalf("a repeated day must keep one banner, got %d: %q", n, got)
	}
	if n := strings.Count(got, "Ngày 12/10/2026"); n != 1 {
		t.Fatalf("a new day must keep its banner, got %d: %q", n, got)
	}
}

func TestEntryAlignmentAndMine(t *testing.T) {
	if (entry{kind: kindChat}).rightAligned() {
		t.Fatal("another's chat must sit left")
	}
	if !(entry{kind: kindMine}).rightAligned() {
		t.Fatal("our echoed message must sit right")
	}
	if !(entry{kind: kindLocal}).rightAligned() {
		t.Fatal("our pending echo must sit right")
	}
	if (entry{kind: kindNotice}).rightAligned() {
		t.Fatal("a local notice must sit left")
	}
	if (entry{kind: kindSystem}).rightAligned() {
		t.Fatal("a system line must sit left")
	}

	sess := NewSession()
	sess.Username = "Me#0001"
	if !sess.isMine(WireMessage{DisplayName: "Me#0001"}) {
		t.Fatal("our display name must count as ours")
	}
	if sess.isMine(WireMessage{DisplayName: "Other#0002"}) {
		t.Fatal("another display name must not count as ours")
	}
}

func TestReplyCancel(t *testing.T) {
	oldCfg := ClientCfg
	ClientCfg = config.DefaultClientConfig()
	defer func() { ClientCfg = oldCfg }()

	a, sess := newTestApp()
	sess.Conn = &stubConn{}
	sess.Chain.WireIdx = newWireIndex(16)
	sess.Display.TabChat.append(entry{header: "10:00 A • #7:abcd", body: "gốc", height: 7, hashHex: "abcd"})

	sess.dispatch("/reply 7")
	if sess.Pending.ReplyDraft != 7 {
		t.Fatalf("draft not armed: %d", sess.Pending.ReplyDraft)
	}
	a.Update(key("backspace"))
	if sess.Pending.ReplyDraft != 0 {
		t.Fatal("backspace on an empty draft must cancel the reply")
	}

	sess.dispatch("/reply 7")
	a.Update(key("ctrl+c"))
	if sess.Pending.ReplyDraft != 0 {
		t.Fatal("ctrl+c must cancel the reply")
	}

	// A one-shot target is dropped too.
	sess.Pending.PendingReplyTo = 7
	a.Update(key("esc"))
	if sess.Pending.PendingReplyTo != 0 {
		t.Fatal("esc must drop a pending reply target")
	}
}

func TestReplyPreviewLine(t *testing.T) {
	oldCfg := ClientCfg
	ClientCfg = config.DefaultClientConfig()
	defer func() { ClientCfg = oldCfg }()

	a, sess := newTestApp()
	sess.Conn = &stubConn{}
	sess.Chain.WireIdx = newWireIndex(16)
	sess.Display.TabChat.append(entry{header: "10:00 A • #7:abcd", body: "gốc", height: 7, hashHex: "abcd"})
	sess.dispatch("/reply 7")
	if sess.Pending.ReplyDraft != 7 {
		t.Fatalf("draft not armed: %d", sess.Pending.ReplyDraft)
	}
	sess.Display.DisplayMu.Lock()
	line := sess.replyPreviewLine()
	sess.Display.DisplayMu.Unlock()
	status := sess.uiStatus()
	if !strings.Contains(line, "↩ #7") {
		t.Fatalf("reply preview line = %q", line)
	}
	if !strings.Contains(status, "reply") {
		t.Fatalf("the hint belongs in the status line, got %q", status)
	}
	// The app shows the preview above the input, not as a chat entry.
	a.dirty = true
	a.syncReplyLine()
	if a.replyLine != line {
		t.Fatalf("app replyLine = %q, want %q", a.replyLine, line)
	}
	view := stripANSIForFind(a.View().Content)
	if !strings.Contains(view, "↩ #7") {
		t.Fatalf("view must show the reply preview above the input:\n%s", view)
	}
	for _, e := range sess.Display.TabChat.lines {
		if strings.Contains(e.header, "↩ #7") {
			t.Fatal("the preview must not be a chat entry")
		}
	}
}

func TestAppMaybeLoadOlderGuards(t *testing.T) {
	a, sess := newTestApp()
	sess.Chain.WireIdx = newWireIndex(16)
	a.atTopArmed = true
	a.maybeLoadOlder()
	if a.pendingOlder {
		t.Fatal("must not request older history when none is available")
	}

	sess.Conn = &stubConn{}
	sess.Chain.WireIdx.put(WireMessage{ChainHeight: 7})
	a.atTopArmed = true
	a.maybeLoadOlder()
	if !a.pendingOlder {
		t.Fatal("a page-up at the top must request older history")
	}
}

func TestAppTabCycles(t *testing.T) {
	a, _ := newTestApp()
	a.Update(key("tab"))
	if a.active != tabIndexSystem {
		t.Fatalf("tab must move to system, got %d", a.active)
	}
	a.Update(key("tab"))
	if a.active != tabIndexChat {
		t.Fatalf("tab on the last tab must wrap to chat, got %d", a.active)
	}
	a.Update(key("shift+tab"))
	if a.active != tabIndexSystem {
		t.Fatalf("shift+tab must wrap to system, got %d", a.active)
	}
}

func TestAppSlashModeToggle(t *testing.T) {
	a, _ := newTestApp()
	a.Update(key("/"))
	if !a.cmdMode || a.input.Value() != "" {
		t.Fatalf("first slash: cmdMode=%v value=%q", a.cmdMode, a.input.Value())
	}
	// The next character must not be swallowed and must stay slash-free.
	a.Update(key("h"))
	if a.input.Value() != "h" {
		t.Fatalf("char after the slash = %q, want h", a.input.Value())
	}
	a.input.SetValue("")
	a.Update(key("/"))
	if a.cmdMode || a.input.Value() != "//" {
		t.Fatalf("second slash: cmdMode=%v value=%q", a.cmdMode, a.input.Value())
	}
}

func TestAppSlashModeSubmitSendsLiteral(t *testing.T) {
	a, _ := newTestApp()
	for _, ch := range "/help" {
		a.Update(key(string(ch)))
	}
	if !a.cmdMode {
		t.Fatalf("command mode not armed for %q", a.input.Value())
	}
	a.Update(key("enter"))
	select {
	case got := <-a.lines:
		if got != "/help" {
			t.Fatalf("submitted %q, want /help", got)
		}
	default:
		t.Fatal("command must be submitted")
	}
	if a.cmdMode {
		t.Fatal("cmdMode must reset after submit")
	}
}

func TestAppSlashModeEmptySubmitIgnored(t *testing.T) {
	a, _ := newTestApp()
	a.Update(key("/"))
	a.Update(key("enter"))
	select {
	case got := <-a.lines:
		t.Fatalf("bare command mode must not submit, got %q", got)
	default:
	}
	if a.cmdMode {
		t.Fatal("cmdMode must reset after submit")
	}
}

func TestAppSlashMidDraftIsLiteral(t *testing.T) {
	a, _ := newTestApp()
	for _, ch := range "hi" {
		a.Update(key(string(ch)))
	}
	a.Update(key("/"))
	if a.cmdMode || a.input.Value() != "hi/" {
		t.Fatalf("mid-draft slash: cmdMode=%v value=%q", a.cmdMode, a.input.Value())
	}
}

func TestAppSlashWithoutText(t *testing.T) {
	a, _ := newTestApp()
	// A terminal that reports "/" with no Text must still arm command mode.
	a.Update(tea.KeyPressMsg{Code: '/'})
	if a.input.Value() != "" || !a.cmdMode {
		t.Fatalf("slash without text: value=%q cmd=%v", a.input.Value(), a.cmdMode)
	}
}

func TestAppSlashWithModifier(t *testing.T) {
	a, _ := newTestApp()
	// Some layouts report "/" with a modifier and no Text.
	a.Update(tea.KeyPressMsg{Code: '/', Mod: tea.ModShift})
	if a.input.Value() != "" || !a.cmdMode {
		t.Fatalf("slash with modifier: value=%q cmd=%v", a.input.Value(), a.cmdMode)
	}
}

func TestAppShiftSlashOpensHelp(t *testing.T) {
	a, _ := newTestApp()
	// Shift+/ is "?": it must open help, not insert a slash.
	a.Update(tea.KeyPressMsg{Code: '/', Text: "?", Mod: tea.ModShift})
	if !a.showHelp || a.input.Value() != "" {
		t.Fatalf("shift+/ must open help: showHelp=%v value=%q", a.showHelp, a.input.Value())
	}
}

func TestAppHistorySlashRecall(t *testing.T) {
	a, _ := newTestApp()
	a.hist.add("/h")
	a.Update(key("up"))
	if !a.cmdMode || a.input.Value() != "h" {
		t.Fatalf("recall of /h: cmd=%v value=%q (want cmd=true value=h)", a.cmdMode, a.input.Value())
	}
	a.Update(key("enter"))
	select {
	case got := <-a.lines:
		if got != "/h" {
			t.Fatalf("recalled command submitted %q, want /h", got)
		}
	default:
		t.Fatal("recalled command must submit")
	}
}

func TestAppHistoryDoubleSlashStaysLiteral(t *testing.T) {
	a, _ := newTestApp()
	a.hist.add("//h")
	a.Update(key("up"))
	if a.cmdMode || a.input.Value() != "//h" {
		t.Fatalf("recall of //h: cmd=%v value=%q (want cmd=false value=//h)", a.cmdMode, a.input.Value())
	}
}

func TestAppBackspaceExitsCommandMode(t *testing.T) {
	a, _ := newTestApp()
	a.Update(key("/"))
	if !a.cmdMode || a.input.Value() != "" {
		t.Fatalf("setup: cmd=%v value=%q", a.cmdMode, a.input.Value())
	}
	a.Update(key("backspace"))
	if a.cmdMode {
		t.Fatal("backspace on the bare marker must leave command mode")
	}
}

func TestAppBackspaceDeletesThenExits(t *testing.T) {
	a, _ := newTestApp()
	a.Update(key("/"))
	a.Update(key("h"))
	a.Update(key("backspace"))
	if !a.cmdMode || a.input.Value() != "" {
		t.Fatalf("first backspace must only clear the char: cmd=%v value=%q", a.cmdMode, a.input.Value())
	}
	a.Update(key("backspace"))
	if a.cmdMode {
		t.Fatal("second backspace must leave command mode")
	}
}

func TestAppEscExitsCommandMode(t *testing.T) {
	a, _ := newTestApp()
	a.Update(key("/"))
	a.Update(key("h"))
	a.Update(key("esc"))
	if a.cmdMode || a.input.Value() != "" {
		t.Fatalf("esc must clear and leave command mode: cmd=%v value=%q", a.cmdMode, a.input.Value())
	}
}

func TestAppInputBorderModes(t *testing.T) {
	a, _ := newTestApp()
	if got := a.inputMode(); got != inputModeEmpty {
		t.Fatalf("empty draft mode = %d, want %d", got, inputModeEmpty)
	}
	a.Update(key("h"))
	if got := a.inputMode(); got != inputModeText {
		t.Fatalf("text draft mode = %d, want %d", got, inputModeText)
	}
	a.input.SetValue("")
	a.Update(key("/"))
	if got := a.inputMode(); got != inputModeCommand {
		t.Fatalf("command mode = %d, want %d", got, inputModeCommand)
	}
}

func TestAppPromptMarkers(t *testing.T) {
	a, _ := newTestApp()
	if got := a.promptFor(textarea.PromptInfo{LineNumber: 0}); got != "> " {
		t.Fatalf("chat prompt = %q", got)
	}
	a.cmdMode = true
	if got := a.promptFor(textarea.PromptInfo{LineNumber: 0}); got != " /" {
		t.Fatalf("command prompt = %q, want \" /\"", got)
	}
	// A multi-line draft is the full text editor: no prompt on any line.
	a.cmdMode = false
	a.input.SetValue("line one\nline two")
	if got := a.promptFor(textarea.PromptInfo{LineNumber: 0}); got != "" {
		t.Fatalf("full text edit must carry no prompt, got %q", got)
	}
	if got := a.promptFor(textarea.PromptInfo{LineNumber: 1}); got != "" {
		t.Fatalf("continuation must carry no prompt, got %q", got)
	}
}

func TestAppInputModeName(t *testing.T) {
	a, _ := newTestApp()
	if got := a.inputModeName(); got != "chat" {
		t.Fatalf("mode = %q, want chat", got)
	}
	a.Update(key("/"))
	if got := a.inputModeName(); got != "lệnh" {
		t.Fatalf("mode = %q, want lệnh", got)
	}
	a.cmdMode = false
	a.input.SetValue("a\nb")
	if got := a.inputModeName(); got != "soạn nhiều dòng" {
		t.Fatalf("mode = %q, want soạn nhiều dòng", got)
	}
	if got := a.renderFooter(); !strings.Contains(got, "soạn nhiều dòng") {
		t.Fatalf("footer must show the mode, got %q", got)
	}
}

func TestAppChatWrapsLongLines(t *testing.T) {
	a, sess := newTestApp()
	a.width = 24
	a.layout()
	sess.Display.TabChat.append(noticeEntry("alpha bravo charlie delta echo foxtrot golf"))
	a.dirty = true
	a.refreshContent()
	if got := a.viewport.TotalLineCount(); got < 2 {
		t.Fatalf("long line must wrap, total lines = %d", got)
	}
	if view := a.viewport.View(); !strings.Contains(view, "alpha") {
		t.Fatalf("wrapped view must keep the head:\n%s", view)
	}
}

func TestAppMouseWheelScrolls(t *testing.T) {
	a, sess := newTestApp()
	for i := 0; i < 60; i++ {
		sess.Display.TabChat.append(noticeEntry("line"))
	}
	a.dirty = true
	a.refreshContent()
	a.viewport.GotoBottom()
	before := a.viewport.YOffset()
	a.Update(tea.MouseWheelMsg{Button: tea.MouseWheelUp})
	if a.viewport.YOffset() >= before {
		t.Fatalf("wheel up must scroll up: before=%d after=%d", before, a.viewport.YOffset())
	}
}

func TestAppScrollbar(t *testing.T) {
	a, sess := newTestApp()
	a.dirty = true
	a.refreshContent()
	if col := strings.TrimSpace(stripANSIForFind(a.scrollbarColumn())); col != "" {
		t.Fatalf("scrollbar must be blank when the chat fits: %q", col)
	}
	for i := 0; i < 200; i++ {
		sess.Display.TabChat.append(noticeEntry("line"))
	}
	a.dirty = true
	a.refreshContent()
	col := stripANSIForFind(a.scrollbarColumn())
	if !strings.Contains(col, "┃") || !strings.Contains(col, "│") {
		t.Fatalf("scrollbar must have a track and a thumb: %q", col)
	}
	if got := len(strings.Split(col, "\n")); got != a.viewport.Height() {
		t.Fatalf("scrollbar rows = %d, want %d", got, a.viewport.Height())
	}
	a.viewport.GotoBottom()
	if bottom := stripANSIForFind(a.scrollbarColumn()); !strings.HasSuffix(bottom, "┃") {
		t.Fatalf("thumb must reach the bottom: %q", bottom)
	}
	a.viewport.GotoTop()
	if top := stripANSIForFind(a.scrollbarColumn()); !strings.HasPrefix(top, "┃") {
		t.Fatalf("thumb must reach the top: %q", top)
	}
}

func TestAppFooterShowsOnlyState(t *testing.T) {
	a, _ := newTestApp()
	if got := a.renderFooter(); !strings.Contains(got, "?") {
		t.Fatalf("idle footer must point at the help, got %q", got)
	}
	if got := a.renderFooter(); strings.Contains(got, "Enter") || strings.Contains(got, "Tab ") {
		t.Fatalf("idle footer must not list key bindings, got %q", got)
	}
	a.status = "Đang tải lịch sử cũ…"
	if got := a.renderFooter(); !strings.Contains(got, "Đang tải") {
		t.Fatalf("status must show in the footer, got %q", got)
	}
	a.status = ""
	a.prompt = "| reply #12 > "
	if got := a.renderFooter(); !strings.Contains(got, "reply #12") {
		t.Fatalf("reply prompt must show in the footer, got %q", got)
	}
}

func TestAppPromptOnlyOnFirstRow(t *testing.T) {
	a, _ := newTestApp()
	if got := a.promptFor(textarea.PromptInfo{LineNumber: 0}); got != "> " {
		t.Fatalf("first row prompt = %q", got)
	}
	// The textarea asks per display row, so a wrapped long line must not
	// repeat the prompt.
	if got := a.promptFor(textarea.PromptInfo{LineNumber: 1}); got != "" {
		t.Fatalf("a wrapped row must carry no prompt, got %q", got)
	}
}

func TestAppSelectionCopiesContentOnly(t *testing.T) {
	a, sess := newTestApp()
	sess.Conn = &stubConn{}
	sess.Display.TabChat.append(entry{header: "15:04 Alice • #1:abcd", body: "hello world", height: 1, hashHex: "abcd"})
	sess.Display.TabChat.append(entry{header: "15:05 Bob • #2:ef01", body: "tin của mình", height: 2, hashHex: "ef01", kind: kindMine})
	a.dirty = true
	a.refreshContent()

	a.selActive = true
	a.selAnchor = selPos{row: 0, col: 0}
	a.selCursor = selPos{row: len(a.rows) - 1, col: a.width}
	a.dirty = true
	a.refreshContent()

	got := a.selectionText()
	if strings.ContainsAny(got, "│╭╮╰╯") {
		t.Fatalf("the copy must not carry frame characters: %q", got)
	}
	if !strings.Contains(got, "hello world") || !strings.Contains(got, "tin của mình") {
		t.Fatalf("the copy must hold the content: %q", got)
	}
	raw := a.View().Content
	if !strings.Contains(raw, "48;5;238") {
		t.Fatal("the selection must highlight the content cells, not the frame")
	}
	// A right-aligned block keeps its inset and frame unhighlighted.
	if !strings.Contains(raw, "48;5;238mtin của mình") {
		t.Fatalf("the highlight must start at the text, not the inset: %q", raw)
	}
	if strings.Contains(raw, "48;5;238m│") || strings.Contains(raw, "48;5;238m╭") {
		t.Fatalf("no frame cell may be highlighted: %q", raw)
	}
}

func TestAppDragDoesNotRebuildContent(t *testing.T) {
	a, sess := newTestApp()
	sess.Conn = &stubConn{}
	sess.Display.TabChat.append(entry{header: "15:04 A • #1:ab", body: "hello", height: 1, hashHex: "ab"})
	a.dirty = true
	a.refreshContent()

	a.selActive = true
	a.selAnchor = selPos{row: 0, col: 0}
	a.dirty = false
	a.Update(tea.MouseMotionMsg{X: 3, Y: 2, Button: tea.MouseLeft})
	if a.dirty {
		t.Fatal("a drag motion must not force a full content rebuild")
	}
	if a.selCursor == a.selAnchor {
		t.Fatal("a drag motion must still move the selection")
	}
}

func TestAppJumpToLatest(t *testing.T) {
	a, sess := newTestApp()
	for i := 0; i < 60; i++ {
		sess.Display.TabChat.append(entry{header: "line", body: "x", height: uint64(i + 1)})
	}
	a.dirty = true
	a.refreshContent()
	a.viewport.GotoTop()
	if a.viewport.AtBottom() {
		t.Fatal("setup: the viewport must start scrolled up")
	}
	if !strings.Contains(a.renderFooter(), "về mới nhất") {
		t.Fatalf("a scrolled-up footer must offer the jump: %q", a.renderFooter())
	}
	a.jumpToLatest()
	if !a.viewport.AtBottom() {
		t.Fatal("jumpToLatest must reach the bottom")
	}
	if strings.Contains(a.renderFooter(), "về mới nhất") {
		t.Fatal("the hint must clear once back at the latest line")
	}
}

func TestAppSubmitJumpsToLatest(t *testing.T) {
	a, sess := newTestApp()
	for i := 0; i < 60; i++ {
		sess.Display.TabChat.append(entry{header: "line", body: "x", height: uint64(i + 1)})
	}
	a.dirty = true
	a.refreshContent()
	a.viewport.GotoTop()
	if a.viewport.AtBottom() {
		t.Fatal("setup: the viewport must start scrolled up")
	}
	a.input.SetValue("xin chào")
	a.submit()
	if !a.viewport.AtBottom() {
		t.Fatal("sending must pull the chat back to the newest line")
	}
}

func TestAppTabClickSwitches(t *testing.T) {
	a, _ := newTestApp()
	click := func(want int) {
		x := -1
		for col := 0; col < a.width; col++ {
			if i, ok := a.tabAt(col); ok && i == want {
				x = col
				break
			}
		}
		if x < 0 {
			t.Fatalf("tab %d must be clickable", want)
		}
		model, _ := a.Update(tea.MouseClickMsg{X: x, Y: 0, Button: tea.MouseLeft})
		next, ok := model.(*app)
		if !ok {
			t.Fatal("Update must return the app model")
		}
		if next.active != want {
			t.Fatalf("click at x=%d must switch to tab %d, got %d", x, want, next.active)
		}
	}
	click(1)
	click(0)
	// A click past the last label belongs to no tab and must not switch.
	model, _ := a.Update(tea.MouseClickMsg{X: a.width - 1, Y: 0, Button: tea.MouseLeft})
	if app, ok := model.(*app); ok && app.active != 0 {
		t.Fatalf("a click off the labels must not switch tabs, got %d", app.active)
	}
}

func TestAppExpandTrailerClick(t *testing.T) {
	sess, out := behaviorSession(t)
	seedIndexedWire(sess, 42)
	sess.Display.TabChat.append(entry{body: "| Alice: hello wo" + expandTrailer(42), height: 42, collapsed: true})
	a := newApp(sess)
	a.width, a.height = 80, 24
	a.layout()
	a.dirty = true
	a.refreshContent()
	if len(a.trailers) != 1 {
		t.Fatalf("the folded block must expose one trailer, got %d", len(a.trailers))
	}
	span := a.trailers[0]
	y := span.row - a.viewport.YOffset() + chatTop

	// The bracket is a button: clicking it unfolds the block where it sits.
	if _, cmd := a.Update(tea.MouseClickMsg{X: span.start, Y: y, Button: tea.MouseLeft}); cmd == nil {
		t.Fatal("a click on the trailer must run the expand")
	} else {
		cmd()
	}
	lines := sess.Display.TabChat.lines
	if len(lines) != 1 {
		t.Fatalf("unfolding in place must not append a block, got %d entries", len(lines))
	}
	if lines[0].collapsed || !strings.Contains(lines[0].body, "hello world") {
		t.Fatalf("the click must unfold the block in place: %+v", lines[0])
	}
	if got := out.String(); strings.Contains(got, "Mở rộng") {
		t.Fatalf("an in-place unfold must not nest a new block: %q", got)
	}

	// Left of the bracket it stays a plain selection.
	model, cmd := a.Update(tea.MouseClickMsg{X: span.start - 1, Y: y, Button: tea.MouseLeft})
	if cmd != nil {
		t.Fatal("a click left of the bracket must not expand")
	}
	if next, ok := model.(*app); !ok || !next.selActive {
		t.Fatal("a click off the trailer must still start a selection")
	}
}

func TestAppTrailerHoverUnderlines(t *testing.T) {
	sess, _ := behaviorSession(t)
	seedIndexedWire(sess, 42)
	sess.Display.TabChat.append(entry{body: "| Alice: hello wo" + expandTrailer(42), height: 42, collapsed: true})
	a := newApp(sess)
	a.width, a.height = 80, 24
	a.layout()
	a.dirty = true
	a.refreshContent()
	if len(a.trailers) != 1 {
		t.Fatalf("the folded block must expose one trailer, got %d", len(a.trailers))
	}
	span := a.trailers[0]
	y := span.row - a.viewport.YOffset() + chatTop

	// The row that draws the trailer, with its escapes intact.
	trailerRow := func() string {
		for _, line := range strings.Split(a.View().Content, "\n") {
			if strings.Contains(ansi.Strip(line), expandMarker) {
				return line
			}
		}
		return ""
	}
	if strings.Contains(trailerRow(), "\x1b[4m") {
		t.Fatal("nothing is hovered yet")
	}
	a.Update(tea.MouseMotionMsg{X: span.start, Y: y})
	if !strings.Contains(trailerRow(), "\x1b[4m") {
		t.Fatalf("a pointer on the trailer must underline it: %q", trailerRow())
	}
	a.Update(tea.MouseMotionMsg{X: span.start - 1, Y: y})
	if strings.Contains(trailerRow(), "\x1b[4m") {
		t.Fatal("the underline must clear when the pointer leaves the bracket")
	}
}

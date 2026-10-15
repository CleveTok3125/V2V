package main

import (
	"io"
	"strings"
	"testing"

	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
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
	if !a.cmdMode || a.input.Value() != "/" {
		t.Fatalf("first slash: cmdMode=%v value=%q", a.cmdMode, a.input.Value())
	}
	// The next character must not be swallowed.
	a.Update(key("h"))
	if a.input.Value() != "/h" {
		t.Fatalf("char after the slash = %q, want /h", a.input.Value())
	}
	a.input.SetValue("/")
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

func TestAppPromptMarkers(t *testing.T) {
	a, _ := newTestApp()
	if got := a.promptFor(textarea.PromptInfo{LineNumber: 0}); got != "> " {
		t.Fatalf("first-line prompt = %q", got)
	}
	if got := a.promptFor(textarea.PromptInfo{LineNumber: 1}); got != "..." {
		t.Fatalf("continuation prompt = %q", got)
	}
	a.cmdMode = true
	if got := a.promptFor(textarea.PromptInfo{LineNumber: 0}); got != "> " {
		t.Fatalf("command-mode prompt must stay the input marker, got %q", got)
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

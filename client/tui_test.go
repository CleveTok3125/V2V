package main

import (
	"io"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func newTestApp() (*app, *Session) {
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

package main

import (
	"io"
	"strings"
	"testing"
)

// fakeTerm is a scripted inputTerminal for tests.
type fakeTerm struct {
	lines   []string
	errs    map[int]error
	calls   int
	prompts []string
}

func (f *fakeTerm) ReadLine() (string, error) {
	defer func() { f.calls++ }()
	if err, ok := f.errs[f.calls]; ok {
		return "", err
	}
	if f.calls >= len(f.lines) {
		return "", io.EOF
	}
	return f.lines[f.calls], nil
}

func (f *fakeTerm) SetPrompt(p string) { f.prompts = append(f.prompts, p) }
func (f *fakeTerm) Refresh()           {}
func (f *fakeTerm) Close()             {}
func (f *fakeTerm) Writer() io.Writer  { return io.Discard }

// chatSpy reads the chat tab the way the app paints it: muted lines stay
// out, so a notice the live chat hides cannot leak into an assertion.
type chatSpy struct{ sess *Session }

func (c *chatSpy) String() string { return c.sess.visibleChat() }
func (c *chatSpy) Len() int       { return len(c.sess.visibleChat()) }

// visibleChat renders the visible stream (tab 1).
func (s *Session) visibleChat() string {
	s.Display.DisplayMu.Lock()
	defer s.Display.DisplayMu.Unlock()
	var sb strings.Builder
	for _, e := range s.Display.TabChat.lines {
		if e.muted {
			continue
		}
		sb.WriteString(e.text())
	}
	return sb.String()
}

// queueTestSession builds a session with the tab buffers a display expects.
func queueTestSession(t *testing.T) (*Session, *chatSpy) {
	t.Helper()
	sess := NewSession()
	sess.Display.Term = &fakeTerm{}
	sess.Display.ActiveTab = TabChat
	sess.Display.TabSys = newTabBuffer(100, 100000)
	sess.Display.TabChat = newTabBuffer(100, 100000)
	sess.Chain.TipPath = t.TempDir() + "/tip.json"
	return sess, &chatSpy{sess: sess}
}

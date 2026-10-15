package main

import (
	"bytes"
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

// queueTestSession builds a session whose display renders into a buffer.
func queueTestSession(t *testing.T) (*Session, *bytes.Buffer) {
	t.Helper()
	sess := NewSession()
	var out bytes.Buffer
	sess.Display.Out = &out
	sess.Display.Term = &fakeTerm{}
	sess.Display.ActiveTab = TabChat
	sess.Display.TabSys = newTabBuffer(100, 100000)
	sess.Display.TabChat = newTabBuffer(100, 100000)
	sess.Chain.TipPath = t.TempDir() + "/tip.json"
	return sess, &out
}

// flushOutputNow renders the visible stream (tab 1) into Display.Out, which is
// what the app paints, so tests can assert the chat text.
func (s *Session) flushOutputNow() {
	s.Display.DisplayMu.Lock()
	defer s.Display.DisplayMu.Unlock()
	if s.Display.Out == nil {
		return
	}
	var sb strings.Builder
	for _, e := range s.Display.TabChat.lines {
		if e.muted {
			continue
		}
		sb.WriteString(e.text())
	}
	_, _ = s.Display.Out.Write([]byte(sb.String()))
}

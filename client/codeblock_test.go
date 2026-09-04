package main

import (
	"errors"
	"io"
	"testing"
)

// fakeTerm feeds scripted lines (or errors) to collectCodeblock and
// records prompt changes.
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

func TestCollectCodeblockNormal(t *testing.T) {
	ft := &fakeTerm{lines: []string{"abc", "```"}}
	text, canceled := collectCodeblock(ft, "```")
	if canceled {
		t.Fatal("unexpected cancel")
	}
	if text != "```\nabc\n```" {
		t.Fatalf("got %q", text)
	}
}

func TestCollectCodeblockCancel(t *testing.T) {
	ft := &fakeTerm{lines: []string{"abc"}, errs: map[int]error{1: ErrInputCancel}}
	text, canceled := collectCodeblock(ft, "```")
	if !canceled {
		t.Fatal("expected cancel")
	}
	if text != "" {
		t.Fatalf("canceled block must be empty, got %q", text)
	}
}

func TestCollectCodeblockEOF(t *testing.T) {
	ft := &fakeTerm{lines: []string{"abc"}, errs: map[int]error{1: io.EOF}}
	_, canceled := collectCodeblock(ft, "```")
	if !canceled {
		t.Fatal("expected cancel on EOF")
	}
}

func TestCollectCodeblockCancelRestoresPrompt(t *testing.T) {
	ft := &fakeTerm{errs: map[int]error{0: errors.New("boom")}}
	collectCodeblock(ft, "```")
	if len(ft.prompts) != 2 || ft.prompts[0] != "| ... " || ft.prompts[1] != "| > " {
		t.Fatalf("prompt not restored: %q", ft.prompts)
	}
}

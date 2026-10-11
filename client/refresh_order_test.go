package main

// The terminal has two writers that must not interleave: rendered lines go
// out through the output queue (drained by a goroutine), while Refresh draws
// the prompt straight from the caller. Refreshing with a block still queued
// puts the prompt inside that block's byte sequence, which is how a notice or
// a prompt ends up wedged between the lines of one message.

import (
	"io"
	"strings"
	"testing"
)

// orderTerm records how much output had reached the terminal when Refresh ran.
type orderTerm struct {
	out       *strings.Builder
	atRefresh int
	refreshes int
}

func (t *orderTerm) ReadLine() (string, error) { return "", io.EOF }
func (t *orderTerm) SetPrompt(string)          {}
func (t *orderTerm) Close()                    {}
func (t *orderTerm) Writer() io.Writer         { return t.out }
func (t *orderTerm) Refresh() {
	t.refreshes++
	t.atRefresh = t.out.Len()
}

// TestRefreshDrainsTheQueueFirst pins the ordering. The queue is seeded
// directly so no drainer goroutine is involved: the only thing that can put
// those bytes on the terminal inside refreshTerminal is the flush.
func TestRefreshDrainsTheQueueFirst(t *testing.T) {
	const block = "| 12:00 A • #1:ab12\n|   body\n"
	var out strings.Builder
	term := &orderTerm{out: &out}
	sess := &Session{}
	sess.Display.Out = &out
	sess.Display.Term = term
	sess.Display.OutBuf = []byte(block)

	sess.refreshTerminal()

	if term.refreshes != 1 {
		t.Fatalf("refreshes = %d, want 1", term.refreshes)
	}
	if term.atRefresh != len(block) {
		t.Fatalf("Refresh saw %d of %d queued bytes: the prompt was drawn inside the block",
			term.atRefresh, len(block))
	}
	if got := out.String(); got != block {
		t.Fatalf("terminal output = %q, want %q", got, block)
	}
	if n := len(sess.Display.OutBuf); n != 0 {
		t.Fatalf("queue still holds %d bytes after the refresh", n)
	}
}

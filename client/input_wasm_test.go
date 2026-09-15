//go:build js

package main

import (
	"strings"
	"syscall/js"
	"testing"
	"time"
)

// newWasmTestTerm builds a wasmTerm whose JS output is captured instead
// of reaching a terminal. Runs under node via GOOS=js GOARCH=wasm.
func newWasmTestTerm(t *testing.T) (*wasmTerm, *[]string) {
	t.Helper()
	var wrote []string
	fn := js.FuncOf(func(_ js.Value, args []js.Value) any {
		wrote = append(wrote, args[0].String())
		return nil
	})
	t.Cleanup(fn.Release)
	term := &wasmTerm{
		out:      jsOutputWriter{fn: fn.Value},
		keyCh:    make(chan string, 64),
		lineCh:   make(chan string, 4),
		cancelCh: make(chan struct{}, 1),
	}
	term.SetPrompt("| > ")
	return term, &wrote
}

func (t *wasmTerm) testLine() string { return string(t.line) }

func TestWasmTermEdit(t *testing.T) {
	term, _ := newWasmTestTerm(t)
	for _, r := range "hello" {
		term.insertRune(r)
	}
	if term.testLine() != "hello" || term.cur != 5 {
		t.Fatalf("typed = %q cur = %d", term.testLine(), term.cur)
	}
	term.cursorLeft(2)
	term.deletePrev()
	if term.testLine() != "helo" || term.cur != 2 {
		t.Fatalf("after left2+backspace = %q cur = %d", term.testLine(), term.cur)
	}
	term.deleteAt()
	if term.testLine() != "heo" {
		t.Fatalf("after delete = %q", term.testLine())
	}
	term.home()
	term.insertRune('S')
	if term.testLine() != "Sheo" {
		t.Fatalf("after home+insert = %q", term.testLine())
	}
	term.end()
	if term.cur != 4 {
		t.Fatalf("end cur = %d, want 4", term.cur)
	}
	term.clearLine()
	if term.testLine() != "" || term.cur != 0 {
		t.Fatalf("after clear = %q cur = %d", term.testLine(), term.cur)
	}
	// Clamp both directions: no negative cursor, no overrun.
	term.cursorLeft(99)
	if term.cur != 0 {
		t.Fatalf("left clamp cur = %d", term.cur)
	}
	term.insertRune('x')
	term.cursorRight(99)
	if term.cur != 1 {
		t.Fatalf("right clamp cur = %d", term.cur)
	}
}

func TestWasmCSI(t *testing.T) {
	term, _ := newWasmTestTerm(t)
	for _, r := range "abcd" {
		term.insertRune(r)
	}
	term.dispatchEsc("\x1b[D")
	term.dispatchEsc("\x1b[D")
	if term.cur != 2 {
		t.Fatalf("2x left cur = %d, want 2", term.cur)
	}
	term.dispatchEsc("\x1b[2C")
	if term.cur != 4 {
		t.Fatalf("CSI 2C cur = %d, want 4", term.cur)
	}
	term.dispatchEsc("\x1b[H")
	if term.cur != 0 {
		t.Fatalf("home cur = %d", term.cur)
	}
	term.dispatchEsc("\x1b[3~")
	if term.testLine() != "bcd" {
		t.Fatalf("delete key line = %q", term.testLine())
	}
	term.dispatchEsc("\x1bOF")
	if term.cur != 3 {
		t.Fatalf("SS3 end cur = %d", term.cur)
	}
	for in, want := range map[string]int{"": 1, "0": 1, "3": 3, "1;5": 1, "abc": 1} {
		if got := csiInt(in); got != want {
			t.Errorf("csiInt(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestWasmLineLoopSubmit(t *testing.T) {
	term, wrote := newWasmTestTerm(t)
	go term.lineLoop()
	term.keyCh <- "hi\x7fyo\r"
	// Stopped timer: an unconsumed time.After would fire after
	// os.Exit under js/wasm and throw in the node bridge.
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	select {
	case line := <-term.lineCh:
		if line != "hyo" {
			t.Fatalf("submitted = %q, want hyo", line)
		}
	case <-timer.C:
		t.Fatal("lineLoop never submitted")
	}
	if len(*wrote) == 0 {
		t.Fatal("no output reached the JS bridge")
	}
	joined := strings.Join(*wrote, "")
	if !strings.Contains(joined, "| > ") {
		t.Error("prompt missing from bridge output")
	}
}

func TestWasmDialWSNoAPI(t *testing.T) {
	// Hide the browser API: with no WebSocket global the dial must
	// fail with the unavailable error instead of hanging.
	hadWS := js.Global().Get("WebSocket")
	js.Global().Set("WebSocket", js.Undefined())
	t.Cleanup(func() { js.Global().Set("WebSocket", hadWS) })

	oldProxy, oldAsk := CLI.Proxy, CLI.AskProxy
	CLI.Proxy, CLI.AskProxy = "socks5://127.0.0.1:1080", true
	t.Cleanup(func() { CLI.Proxy, CLI.AskProxy = oldProxy, oldAsk })

	_, _, err := dialWS("wss://chat.example.com/ws")
	if err == nil || !strings.Contains(err.Error(), "không khả dụng") {
		t.Fatalf("dialWS without WebSocket API = %v, want unavailable error", err)
	}
}

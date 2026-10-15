//go:build js

package main

import (
	"io"
	"sync"
	"syscall/js"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/colorprofile"
)

// wasmInput is the io.Reader Bubble Tea parses keystrokes from. The browser
// terminal pushes raw bytes from its onData callback; Read blocks until
// they arrive, so the reader never busy-loops.
type wasmInput struct {
	mu     sync.Mutex
	cond   *sync.Cond
	buf    []byte
	closed bool
}

func newWasmInput() *wasmInput {
	q := &wasmInput{}
	q.cond = sync.NewCond(&q.mu)
	return q
}

func (q *wasmInput) push(s string) {
	q.mu.Lock()
	q.buf = append(q.buf, s...)
	q.cond.Signal()
	q.mu.Unlock()
}

func (q *wasmInput) Read(p []byte) (int, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for len(q.buf) == 0 && !q.closed {
		q.cond.Wait()
	}
	if len(q.buf) == 0 {
		return 0, io.EOF
	}
	n := copy(p, q.buf)
	q.buf = q.buf[n:]
	return n, nil
}

// wasmOutput forwards the renderer's bytes to the browser terminal.
type wasmOutput struct{}

func (wasmOutput) Write(p []byte) (int, error) {
	if fn := js.Global().Get("v2vOutput"); fn.Truthy() {
		fn.Invoke(string(p))
	}
	return len(p), nil
}

// runTUI runs the app against the browser terminal. Bubble Tea parses the
// keystroke bytes and paints the screen, so no terminal emulation lives in
// Go. Colour is forced because the wasm build has no TTY to detect from.
func runTUI(a *app) error {
	input := newWasmInput()

	// The grid size arrives from the page. It is tracked even before the
	// program runs: the page's boot fit lands here, and the first frame must
	// already be the real grid or the output is laid out for 80x24.
	var sizeMu sync.Mutex
	cols, rows := 80, 24
	js.Global().Set("v2vSendKeys", js.FuncOf(func(_ js.Value, args []js.Value) any {
		if len(args) > 0 {
			input.push(args[0].String())
		}
		return nil
	}))
	js.Global().Set("v2vSetSize", js.FuncOf(func(_ js.Value, args []js.Value) any {
		if len(args) >= 2 {
			c, r := args[0].Int(), args[1].Int()
			sizeMu.Lock()
			cols, rows = c, r
			sizeMu.Unlock()
			a.sink.send(tea.WindowSizeMsg{Width: c, Height: r})
		}
		return nil
	}))
	js.Global().Set("v2vRefresh", js.FuncOf(func(js.Value, []js.Value) any {
		a.sink.send(uiRefreshMsg{})
		return nil
	}))
	// Ask the page to fit now, then start with the size it reports.
	if fn := js.Global().Get("v2vWasmReady"); fn.Truthy() {
		fn.Invoke()
	}
	sizeMu.Lock()
	width, height := cols, rows
	sizeMu.Unlock()

	p := tea.NewProgram(a,
		tea.WithInput(input),
		tea.WithOutput(wasmOutput{}),
		tea.WithColorProfile(colorprofile.TrueColor),
		tea.WithEnvironment([]string{"TERM=xterm-256color", "COLORTERM=truecolor"}),
		tea.WithWindowSize(width, height),
	)
	a.sink.attach(p)

	_, err := p.Run()
	return err
}

// notifyQuit tells the page to reload back to the connect panel.
func notifyQuit() {
	if fn := js.Global().Get("v2vExit"); fn.Truthy() {
		fn.Invoke()
	}
}

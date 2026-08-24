//go:build js

package main

import (
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall/js"
)

// jsOutputWriter forwards every byte chunk written by the Go client to the
// JS-side terminal emulator via the window.v2vOutput callback.
type jsOutputWriter struct {
	fn js.Value
}

func (w jsOutputWriter) Write(p []byte) (int, error) {
	w.fn.Invoke(string(p))
	return len(p), nil
}

// wasmTerm is a tiny line editor running inside the WASM client. The browser
// only forwards raw keystrokes (window.v2vSendKeys); this type does the
// echoing, cursor movement, backspace/delete, escape handling and prompt
// drawing, so the JS glue stays minimal and the input state lives in a
// single place.
type wasmTerm struct {
	out       jsOutputWriter
	keyCh     chan string
	lineCh    chan string
	keysFn    js.Func
	refreshFn js.Func
	setSizeFn js.Func

	// termCols mirrors the live xterm grid width (bridged by v2vSetSize);
	// every redraw needs it to reason about soft-wrapped multi-row drafts.
	termCols atomic.Int32

	mu     sync.Mutex
	prompt string
	active bool
	line   []rune
	cur    int

	esc string
}

func newInputTerminal() (inputTerminal, error) {
	t := &wasmTerm{
		out:    jsOutputWriter{fn: js.Global().Get("v2vOutput")},
		keyCh:  make(chan string, 64),
		lineCh: make(chan string, 4),
		prompt: "| > ",
	}

	t.keysFn = js.FuncOf(func(this js.Value, args []js.Value) any {
		if len(args) > 0 {
			s := args[0].String()
			select {
			case t.keyCh <- s:
			default: // never block the JS event loop
			}
		}
		return nil
	})
	js.Global().Set("v2vSendKeys", t.keysFn)

	// v2vRefresh lets the JS side (resize/orientation changes) ask the Go
	// client to repaint the prompt + draft at the new grid geometry.
	t.refreshFn = js.FuncOf(func(this js.Value, args []js.Value) any {
		go t.Refresh()
		return nil
	})
	js.Global().Set("v2vRefresh", t.refreshFn)

	// v2vSetSize receives the live grid size from the JS terminal after
	// every fit, so redraws know how many columns a draft row spans.
	t.setSizeFn = js.FuncOf(func(this js.Value, args []js.Value) any {
		if len(args) > 0 {
			if c := args[0].Int(); c > 0 {
				t.termCols.Store(int32(c))
			}
		}
		return nil
	})
	js.Global().Set("v2vSetSize", t.setSizeFn)

	go t.lineLoop()

	// Tell JS the editor is wired up; the page re-fits now that Go can be
	// told the real grid size (before any output is produced).
	if fn := js.Global().Get("v2vWasmReady"); fn.Truthy() {
		fn.Invoke()
	}

	return t, nil
}

// Writer is used for all chat output (greeting, incoming messages, own
// messages). When an input prompt is on screen, the draft line is moved out
// of the way and redrawn after the new text, so incoming messages never
// corrupt what the user is typing.
func (t *wasmTerm) Writer() io.Writer { return t }

func (t *wasmTerm) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if !t.active {
		return t.out.Write(p)
	}

	// Move the whole draft block (which may span several rows when the
	// prompt + draft exceed the terminal width) out of the way, print the
	// incoming text below it, then repaint the draft.
	up := t.wipeOffsetLocked()
	t.wipeLocked(up)
	t.out.Write(p)
	if len(p) == 0 || p[len(p)-1] != '\n' {
		t.out.Write([]byte("\r\n"))
	}
	t.paintFreshLocked()
	return len(p), nil
}

func (t *wasmTerm) ReadLine() (string, error) {
	t.mu.Lock()
	t.active = true
	t.wipeLocked(0)
	t.paintFreshLocked()
	t.mu.Unlock()

	line, ok := <-t.lineCh
	if !ok {
		return "", io.EOF
	}
	return line, nil
}

func (t *wasmTerm) SetPrompt(p string) {
	t.mu.Lock()
	t.prompt = p
	t.mu.Unlock()
}

// Refresh repaints the draft block, e.g. after the JS terminal resized and
// reflowed the buffer: with the new column count the cursor's row offset is
// recomputed from scratch, matching where xterm placed the logical position.
func (t *wasmTerm) Refresh() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.active {
		return
	}
	up := t.wipeOffsetLocked()
	t.wipeLocked(up)
	t.paintFreshLocked()
}

func (t *wasmTerm) Close() {
	t.keysFn.Release()
	t.refreshFn.Release()
	t.setSizeFn.Release()
}

// notifyQuit lets the platform hook do any cleanup when the client quits.
// On the web build the page is reloaded so the user lands back on the
// connect panel; the JS side exposes window.v2vExit for that purpose.
func notifyQuit() {
	if fn := js.Global().Get("v2vExit"); fn.Truthy() {
		fn.Invoke()
	}
}

// lineLoop consumes raw keystrokes and emulates a simple text-editor line
// discipline: printable runes insert at the cursor, arrow keys move the
// cursor, Home/End jump to the line edges, Backspace/Delete remove a rune,
// Ctrl+C clears the line and Enter submits it.
func (t *wasmTerm) lineLoop() {
	for keys := range t.keyCh {
		for _, r := range keys {
			if t.esc != "" {
				t.esc += string(r)
				switch t.esc[1] {
				case '[': // CSI: consume until the final byte 0x40..0x7e
					// ('[' itself is 0x5b and falls in that range, so the
					// sequence must have at least one byte after it).
					if len(t.esc) >= 3 && r >= 0x40 && r <= 0x7e {
						t.dispatchEsc(t.esc)
						t.esc = ""
					}
				case 'O': // SS3: exactly one more byte
					if len(t.esc) >= 3 {
						t.dispatchEsc(t.esc)
						t.esc = ""
					}
				default:
					t.dispatchEsc(t.esc)
					t.esc = ""
				}
				continue
			}

			switch r {
			case '\x1b':
				t.esc = "\x1b"
			case '\r', '\n':
				t.mu.Lock()
				up := t.wipeOffsetLocked()
				line := string(t.line)
				// Repaint the submitted line in one pass so the cursor
				// ends up just below the block, then move to a new line.
				t.wipeLocked(up)
				t.out.Write([]byte(t.prompt))
				t.out.Write([]byte(line))
				t.out.Write([]byte("\r\n"))
				t.line = t.line[:0]
				t.cur = 0
				t.active = false
				t.mu.Unlock()
				t.lineCh <- line
			case '\x7f', '\b':
				t.deletePrev()
			case '\x01': // Ctrl+A: home
				t.home()
			case '\x05': // Ctrl+E: end
				t.end()
			case '\x03':
				// Ctrl+C: clear the current line.
				t.mu.Lock()
				up := t.wipeOffsetLocked()
				t.wipeLocked(up)
				t.line = t.line[:0]
				t.cur = 0
				t.out.Write([]byte(t.prompt))
				t.mu.Unlock()
			default:
				if r >= 0x20 {
					t.insertRune(r)
				}
			}
		}
	}
}

// dispatchEsc handles a complete ESC sequence (CSI or SS3).
func (t *wasmTerm) dispatchEsc(seq string) {
	if len(seq) < 2 {
		return
	}
	switch seq[1] {
	case 'O':
		if len(seq) >= 3 {
			switch seq[2] {
			case 'C':
				t.cursorRight(1)
			case 'D':
				t.cursorLeft(1)
			case 'H':
				t.home()
			case 'F':
				t.end()
			}
		}
	case '[':
		if len(seq) >= 3 {
			t.dispatchCSI(seq[2:])
		}
	}
}

// dispatchCSI parses the body of a CSI sequence (params + final byte).
func (t *wasmTerm) dispatchCSI(body string) {
	final := body[len(body)-1]
	params := body[:len(body)-1]
	switch final {
	case 'C': // right
		t.cursorRight(csiInt(params))
	case 'D': // left
		t.cursorLeft(csiInt(params))
	case 'H': // home
		t.home()
	case 'F': // end
		t.end()
	case '~':
		switch params {
		case "1": // Home
			t.home()
		case "3": // Delete
			t.deleteAt()
		case "4": // End
			t.end()
		}
	}
}

// csiInt returns the leading integer of a CSI parameter run (default 1).
// "1;5C" (Ctrl+Right) is treated as a plain single-step move.
func csiInt(params string) int {
	if i := strings.IndexByte(params, ';'); i >= 0 {
		params = params[:i]
	}
	params = strings.TrimLeft(params, " ?")
	n, err := strconv.Atoi(params)
	if err != nil || n < 1 {
		return 1
	}
	return n
}

func (t *wasmTerm) insertRune(r rune) {
	t.mu.Lock()
	defer t.mu.Unlock()
	up := t.wipeOffsetLocked()
	t.line = append(t.line, 0)
	copy(t.line[t.cur+1:], t.line[t.cur:])
	t.line[t.cur] = r
	t.cur++
	t.repaintLocked(up)
}

func (t *wasmTerm) deletePrev() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.cur <= 0 {
		return
	}
	up := t.wipeOffsetLocked()
	t.line = append(t.line[:t.cur-1], t.line[t.cur:]...)
	t.cur--
	t.repaintLocked(up)
}

func (t *wasmTerm) deleteAt() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.cur >= len(t.line) {
		return
	}
	up := t.wipeOffsetLocked()
	t.line = append(t.line[:t.cur], t.line[t.cur+1:]...)
	t.repaintLocked(up)
}

func (t *wasmTerm) cursorLeft(n int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	up := t.wipeOffsetLocked()
	t.cur -= n
	if t.cur < 0 {
		t.cur = 0
	}
	t.repaintLocked(up)
}

func (t *wasmTerm) cursorRight(n int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	up := t.wipeOffsetLocked()
	t.cur += n
	if t.cur > len(t.line) {
		t.cur = len(t.line)
	}
	t.repaintLocked(up)
}

func (t *wasmTerm) home() {
	t.mu.Lock()
	defer t.mu.Unlock()
	up := t.wipeOffsetLocked()
	t.cur = 0
	t.repaintLocked(up)
}

func (t *wasmTerm) end() {
	t.mu.Lock()
	defer t.mu.Unlock()
	up := t.wipeOffsetLocked()
	t.cur = len(t.line)
	t.repaintLocked(up)
}

// currentCols returns the live terminal width, defaulting to the classic 80
// until the JS side reports the real grid size.
func (t *wasmTerm) currentCols() int {
	if c := int(t.termCols.Load()); c > 0 {
		return c
	}
	return 80
}

// wipeOffsetLocked computes how many rows sit between the block top and the
// cursor for the CURRENT draft state. Callers must capture it before any
// mutation: the erase has to start from where the cursor physically is,
// which reflects the pre-edit content.
func (t *wasmTerm) wipeOffsetLocked() int {
	cells := runeStrCells(t.prompt) + lineCells(t.line[:t.cur])
	return editRowsWithin(cells, t.currentCols())
}

// wipeLocked erases everything from the block top down to the end of the
// screen. up is the row distance to travel first; after this the cursor sits
// at the block origin, column 0.
func (t *wasmTerm) wipeLocked(up int) {
	if up > 0 {
		t.out.Write([]byte(fmt.Sprintf("\x1b[%dA\r", up)))
	} else {
		t.out.Write([]byte("\r"))
	}
	t.out.Write([]byte("\x1b[J"))
}

// paintFreshLocked writes prompt + draft assuming the cursor already sits at
// the block origin; sequential output leaves it exactly at the edit point,
// which keeps mid-line cursors correct across wrapped rows without any
// explicit cursor positioning.
func (t *wasmTerm) paintFreshLocked() {
	t.out.Write([]byte(t.prompt))
	t.out.Write([]byte(string(t.line[:t.cur])))
	t.out.Write([]byte(string(t.line[t.cur:])))
}

// repaintLocked wipes the previous rendering and paints the current one in a
// single sequence.
func (t *wasmTerm) repaintLocked(up int) {
	t.wipeLocked(up)
	t.paintFreshLocked()
}

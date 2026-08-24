//go:build js

package main

import (
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"syscall/js"
	"unicode"
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

	go t.lineLoop()

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

	if t.active {
		t.out.Write([]byte("\r\x1b[K"))
	}
	n, _ := t.out.Write(p)
	if t.active {
		if len(p) == 0 || p[len(p)-1] != '\n' {
			t.out.Write([]byte("\r\n"))
		}
		t.drawLocked()
	}
	return n, nil
}

func (t *wasmTerm) ReadLine() (string, error) {
	t.mu.Lock()
	t.active = true
	t.drawLocked()
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

func (t *wasmTerm) Refresh() {}

func (t *wasmTerm) Close() {
	t.keysFn.Release()
	t.refreshFn.Release()
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
				line := string(t.line)
				t.line = t.line[:0]
				t.cur = 0
				t.active = false
				t.out.Write([]byte("\r\n"))
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
				t.line = t.line[:0]
				t.cur = 0
				t.out.Write([]byte("\r\n"))
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
	t.line = append(t.line, 0)
	copy(t.line[t.cur+1:], t.line[t.cur:])
	t.line[t.cur] = r
	t.cur++
	t.drawLocked()
}

func (t *wasmTerm) deletePrev() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.cur <= 0 {
		return
	}
	t.line = append(t.line[:t.cur-1], t.line[t.cur:]...)
	t.cur--
	t.drawLocked()
}

func (t *wasmTerm) deleteAt() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.cur >= len(t.line) {
		return
	}
	t.line = append(t.line[:t.cur], t.line[t.cur+1:]...)
	t.drawLocked()
}

func (t *wasmTerm) cursorLeft(n int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.cur -= n
	if t.cur < 0 {
		t.cur = 0
	}
	t.drawLocked()
}

func (t *wasmTerm) cursorRight(n int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.cur += n
	if t.cur > len(t.line) {
		t.cur = len(t.line)
	}
	t.drawLocked()
}

func (t *wasmTerm) home() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.cur = 0
	t.drawLocked()
}

func (t *wasmTerm) end() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.cur = len(t.line)
	t.drawLocked()
}

// drawLocked redraws the prompt + draft line and positions the cursor at the
// edit point using display-cell arithmetic (the terminal cursor is cell-based,
// so wide/astral characters count as two cells).
func (t *wasmTerm) drawLocked() {
	t.out.Write([]byte("\r\x1b[K"))
	t.out.Write([]byte(t.prompt))
	t.out.Write([]byte(string(t.line)))
	if cells := lineCells(t.line) - lineCells(t.line[:t.cur]); cells > 0 {
		t.out.Write([]byte(fmt.Sprintf("\x1b[%dD", cells)))
	}
}

func lineCells(rs []rune) int {
	n := 0
	for _, r := range rs {
		n += runeWidth(r)
	}
	return n
}

// runeWidth returns the display cell width of a rune (0 = zero-width,
// 1 = narrow, 2 = East Asian wide/fullwidth). Approximation without pulling
// in an external width table.
func runeWidth(r rune) int {
	if r == 0 {
		return 1
	}
	if unicode.Is(unicode.Mn, r) || unicode.Is(unicode.Me, r) || unicode.Is(unicode.Cf, r) {
		return 0
	}
	switch {
	case r >= 0x1100 && r <= 0x115F, // Hangul Jamo
		r >= 0x2E80 && r <= 0x303E, // CJK Radicals .. CJK Symbols
		r >= 0x3041 && r <= 0x33FF, // Hiragana .. CJK Compatibility
		r >= 0x3400 && r <= 0x4DBF, // CJK Ext A
		r >= 0x4E00 && r <= 0x9FFF, // CJK Unified
		r >= 0xA000 && r <= 0xA4CF, // Yi
		r >= 0xAC00 && r <= 0xD7A3, // Hangul Syllables
		r >= 0xF900 && r <= 0xFAFF, // CJK Compat Ideographs
		r >= 0xFE30 && r <= 0xFE4F, // CJK Compat Forms
		r >= 0xFF00 && r <= 0xFF60, // Fullwidth Forms
		r >= 0xFFE0 && r <= 0xFFE6, // Fullwidth signs
		r > 0xFFFF:                 // astral (emoji, ...)
		return 2
	}
	return 1
}

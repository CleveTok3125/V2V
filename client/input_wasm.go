//go:build js

package main

import (
	"io"
	"sync"
	"syscall/js"
	"unicode/utf8"
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

// wasmTerm is a tiny line discipline running inside the WASM client. The
// browser only forwards raw keystrokes (window.v2vSendKeys); this type does
// the echoing, backspace/escape handling and prompt drawing, so the JS glue
// stays minimal and the input state lives in a single place.
type wasmTerm struct {
	out    jsOutputWriter
	keyCh  chan string
	lineCh chan string
	keysFn js.Func

	mu     sync.Mutex
	prompt string
	active bool
	buf    []byte

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
		t.out.Write([]byte(t.prompt))
		t.out.Write(t.buf)
	}
	return n, nil
}

func (t *wasmTerm) ReadLine() (string, error) {
	t.mu.Lock()
	t.active = true
	t.out.Write([]byte(t.prompt))
	if len(t.buf) > 0 {
		t.out.Write(t.buf)
	}
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

func (t *wasmTerm) Close() { t.keysFn.Release() }

// lineLoop consumes raw keystrokes and emulates a TTY line discipline:
// echoes printable runes, handles backspace, swallows escape sequences,
// sends completed lines to ReadLine on Enter.
func (t *wasmTerm) lineLoop() {
	for keys := range t.keyCh {
		for _, r := range keys {
			if t.esc != "" {
				t.esc += string(r)
				if len(t.esc) == 1 {
					continue
				}
				switch t.esc[1] {
				case '[': // CSI: consume until the final byte 0x40..0x7e
					if r >= 0x40 && r <= 0x7e {
						t.esc = ""
					}
				case 'O': // SS3: exactly one more byte
					if len(t.esc) >= 3 {
						t.esc = ""
					}
				default:
					t.esc = ""
				}
				continue
			}

			switch r {
			case '\x1b':
				t.esc = "\x1b"
			case '\r', '\n':
				t.mu.Lock()
				line := string(t.buf)
				t.buf = t.buf[:0]
				t.active = false
				t.out.Write([]byte("\r\n"))
				t.mu.Unlock()
				t.lineCh <- line
			case '\x7f', '\b':
				t.mu.Lock()
				if len(t.buf) > 0 {
					// Remove the last full rune, not a single byte: a
					// multi-byte character (ế, emoji, ...) occupies one or
					// more display cells, so byte-based slicing would drift
					// the cursor backwards into the prompt on backspace.
					_, size := utf8.DecodeLastRune(t.buf)
					t.buf = t.buf[:len(t.buf)-size]
					t.out.Write([]byte("\r\x1b[K"))
					t.out.Write([]byte(t.prompt))
					t.out.Write(t.buf)
				}
				t.mu.Unlock()
			case '\x03':
				// Ctrl+C: clear the current line.
				t.mu.Lock()
				t.buf = t.buf[:0]
				t.out.Write([]byte("\r\n"))
				t.mu.Unlock()
			default:
				if r >= 0x20 {
					t.mu.Lock()
					t.buf = append(t.buf, []byte(string(r))...)
					t.out.Write([]byte(string(r)))
					t.mu.Unlock()
				}
			}
		}
	}
}
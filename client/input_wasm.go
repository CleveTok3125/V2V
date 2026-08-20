//go:build js

package main

import (
	"io"
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

// wasmTerm bridges the chat loop's inputTerminal to the browser: completed
// lines arrive from JS via window.v2vSendLine, output goes out through the
// JS writer. The wasm build renders the prompt itself (there is no readline).
type wasmTerm struct {
	out     jsOutputWriter
	inputCh chan string
	prompt  string
	sendFn  js.Func
}

func newInputTerminal() (inputTerminal, error) {
	t := &wasmTerm{
		out:     jsOutputWriter{fn: js.Global().Get("v2vOutput")},
		inputCh: make(chan string, 8),
		prompt:  "| > ",
	}

	sendFn := js.FuncOf(func(this js.Value, args []js.Value) any {
		if len(args) > 0 {
			t.inputCh <- args[0].String()
		}
		return nil
	})
	js.Global().Set("v2vSendLine", sendFn)
	t.sendFn = sendFn

	return t, nil
}

func (t *wasmTerm) Writer() io.Writer { return t.out }

func (t *wasmTerm) ReadLine() (string, error) {
	t.out.Write([]byte(t.prompt))
	return <-t.inputCh, nil
}

func (t *wasmTerm) SetPrompt(p string) { t.prompt = p }

func (t *wasmTerm) Refresh() {}

func (t *wasmTerm) Close() { t.sendFn.Release() }
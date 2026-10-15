//go:build js

package main

import (
	"syscall/js"
	"testing"
)

// TestWasmInputQueue pins the byte queue that bridges the browser
// terminal's keystrokes into Bubble Tea's parser: bytes come back in
// order and a blocking read returns once more arrive.
func TestWasmInputQueue(t *testing.T) {
	q := newWasmInput()
	q.push("ab")
	q.push("c")
	buf := make([]byte, 2)
	n, err := q.Read(buf)
	if err != nil || n != 2 || string(buf[:n]) != "ab" {
		t.Fatalf("first read = %q, %v", buf[:n], err)
	}
	n, err = q.Read(buf)
	if err != nil || n != 1 || string(buf[:n]) != "c" {
		t.Fatalf("second read = %q, %v", buf[:n], err)
	}
}

// TestWasmOutputForwards pins the writer that feeds the browser terminal:
// every byte the renderer produces reaches window.v2vOutput.
func TestWasmOutputForwards(t *testing.T) {
	var got string
	fn := js.FuncOf(func(_ js.Value, args []js.Value) any {
		got = args[0].String()
		return nil
	})
	defer fn.Release()
	js.Global().Set("v2vOutput", fn)

	if _, err := (wasmOutput{}).Write([]byte("hi")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if got != "hi" {
		t.Fatalf("v2vOutput got %q", got)
	}
}

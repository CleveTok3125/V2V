//go:build js && wasm

package tea

// suspendSupported is false on js/wasm: a browser has no process suspension.
const suspendSupported = false

// suspendProcess is a no-op on js/wasm.
func suspendProcess() {}

// initInput is a no-op on js/wasm: input arrives from the browser terminal
// through the JavaScript bridge, not from a TTY.
func (p *Program) initInput() error { return nil }

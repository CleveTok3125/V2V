//go:build js && wasm

package tea

// listenForResize is a no-op on js/wasm: resize events come from the browser
// terminal through the JavaScript bridge, not from SIGWINCH.
func (p *Program) listenForResize(done chan struct{}) { close(done) }

//go:build js && wasm

package clipboard

import "errors"

// The browser build carries no clipboard backend: the client wires its own
// bridge, and huh only needs the symbols to link. ReadAll/WriteAll report
// unavailable instead of panicking.
func readAll() (string, error) { return "", errors.New("clipboard unavailable on js/wasm") }

func writeAll(string) error { return errors.New("clipboard unavailable on js/wasm") }

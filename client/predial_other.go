//go:build !js

package main

// preDialPass is a no-op on native: the 429 gate challenge is readable
// from the dial response, so dialManaged handles it directly.
func preDialPass(wsURL string) string { return "" }

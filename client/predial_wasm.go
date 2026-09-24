//go:build js

package main

// preDialPass probes the gate before dialing. The browser WebSocket API
// gives no status/body on a failed handshake, so a wasm client cannot
// see the 429 gate challenge the way the native client does. When the
// server says the gate is required, obtain a lease pass up front so the
// dial carries it.
func preDialPass(wsURL string) string {
	base := gateHTTPBase(wsURL)
	if !gateRequiredNow(base) {
		return ""
	}
	pass, ok := runGateFlow(base, "")
	if !ok {
		return ""
	}
	return pass
}

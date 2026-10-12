//go:build js

package main

import "github.com/CleveTok3125/V2V/lib/clientcore"

// preDialPass probes the gate before dialing. The browser WebSocket API
// gives no status/body on a failed handshake, so a wasm client cannot
// see the 429 gate challenge the way the native client does. When the
// server says the gate is required, obtain a lease pass up front so the
// dial carries it.
func preDialPass(wsURL string) string {
	base := clientcore.GateHTTPBase(wsURL)
	if !clientcore.GateRequiredNow(base, gateConfig("")) {
		return ""
	}
	pass, ok := clientcore.RunGateFlow(base, gateConfig(""))
	if !ok {
		return ""
	}
	return pass
}

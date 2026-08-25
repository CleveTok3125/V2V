//go:build js

package main

import (
	"encoding/json"
	"syscall/js"
	"time"
)

// parseFlags reads the connection config that the web page placed in
// window.v2vConfig. The web build never uses key files; privileged logins go
// through WebAuthn passkeys instead.
func parseFlags() {
	cfg := js.Global().Get("v2vConfig")
	if !cfg.Truthy() {
		return
	}

	CLI.Server = cfg.Get("serverUrl").String()
	CLI.Username = cfg.Get("username").String()
	CLI.Tripcode = cfg.Get("tripcode").String()
	if v := cfg.Get("showJoin"); v.Truthy() {
		CLI.ShowJoin = v.Bool()
	}
	if v := cfg.Get("passkey"); v.Truthy() && v.Bool() {
		CLI.Passkey = true
		CLI.Role = cfg.Get("passkeyRole").String()
	}

	if CLI.Server == "" {
		CLI.Server = js.Global().Get("location").Get("origin").String() + "/"
	}
	if CLI.Username == "" {
		CLI.Username = "Anonymous"
	}

	initAssertionBridge()
}

// assertionCh carries the JSON assertion produced by the page's
// navigator.credentials.get() call back into the Go handshake.
var assertionCh chan string

// initAssertionBridge registers the callback the JS side invokes once the
// passkey ceremony finished (or failed).
func initAssertionBridge() {
	assertionCh = make(chan string, 1)
	fn := js.FuncOf(func(this js.Value, args []js.Value) any {
		payload := ""
		if len(args) > 0 {
			payload = args[0].String()
		}
		select {
		case assertionCh <- payload:
		default:
		}
		return nil
	})
	js.Global().Set("v2vAssertionReady", fn)
}

// webauthnAssertion mirrors the server's AuthPacket passkey fields.
type webauthnAssertion struct {
	PasskeyID string `json:"passkey_id"`
	AuthData  string `json:"passkey_auth_data"`
	ClientData string `json:"passkey_client_data"`
	Sig       string `json:"passkey_sig"`
}

// requestAssertion hands the nonce + role to the page and blocks briefly for
// the ceremony result. An empty return means failure/timeout: the caller
// falls back to a guest login.
func requestAssertion(nonceHex, role string) (webauthnAssertion, bool) {
	fn := js.Global().Get("v2vRequestAssertion")
	if !fn.Truthy() {
		return webauthnAssertion{}, false
	}
	select {
	case assertionCh <- "": // drain any stale result first
	default:
	}
	fn.Invoke(nonceHex, role)

	select {
	case payload := <-assertionCh:
		if payload == "" {
			return webauthnAssertion{}, false
		}
		var a webauthnAssertion
		if err := json.Unmarshal([]byte(payload), &a); err != nil || a.PasskeyID == "" {
			return webauthnAssertion{}, false
		}
		return a, true
	case <-time.After(10 * time.Second):
		return webauthnAssertion{}, false
	}
}

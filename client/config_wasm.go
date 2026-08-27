//go:build js

package main

import (
	"encoding/json"
	"fmt"
	"syscall/js"
	"time"
)

// webPasskey carries the page's intent to log in via WebAuthn.
var webPasskey struct {
	Enabled bool
	Role    string
}

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
		webPasskey.Enabled = true
		webPasskey.Role = cfg.Get("passkeyRole").String()
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

// setWasmStatus shows a message in the login panel's status line when
// available (web), falling back to terminal output otherwise. Returns true
// if the JS status line was used.
func setWasmStatus(msg string, isError bool) bool {
	if fn := js.Global().Get("v2vSetStatus"); fn.Truthy() {
		fn.Invoke(msg, isError)
		return true
	}
	return false
}

// showWasmStatus tries to show an auth failure in the web panel; returns
// true if it was handled via JS.
func showWasmStatus(msg string, isError bool) bool {
	return setWasmStatus(msg, isError)
}

// parkForever keeps the WASM runtime alive after a failed login attempt
// so late browser events (passkey dialog dismissal, timers) resume a live
// runtime instead of a dead one.
func parkForever() { select {} }

// applyWebPasskey runs the browser ceremony when the page requested a
// passkey login. Returns false only on failure — the caller treats that as
// fatal (no silent guest fallback) but will show the error in the status
// line instead of reloading the page.
func applyWebPasskey(resp *AuthPacket, nonceHex string) bool {
	if !webPasskey.Enabled {
		return true // passkey not requested; continue as guest
	}
	fmt.Printf("🔑 Đang chờ passkey cho role [%s]...\n", webPasskey.Role)
	setWasmStatus("Đang chờ xác thực passkey…", false)
	resp.Role = webPasskey.Role
	a, ok := requestAssertion(nonceHex, webPasskey.Role)
	if !ok {
		msg := "❌ Passkey thất bại/hủy."
		fmt.Println(msg)
		setWasmStatus(msg, true)
		return false
	}
	resp.PasskeyID = a.PasskeyID
	resp.PasskeyAuthData = a.AuthData
	resp.PasskeyClientData = a.ClientData
	resp.PasskeySig = a.Sig
	fmt.Println("✅ Passkey đã ký — gửi xác thực...")
	setWasmStatus("Đang gửi xác thực…", false)
	return true
}

// requestAssertion hands the nonce + role to the page and blocks briefly for
// the ceremony result. ok=false means failure/timeout.
func requestAssertion(nonceHex, role string) (webauthnAssertion, bool) {
	fn := js.Global().Get("v2vRequestAssertion")
	if !fn.Truthy() {
		return webauthnAssertion{}, false
	}
	select {
	case <-assertionCh: // drop any stale result from a previous attempt
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
	case <-time.After(70 * time.Second):
		return webauthnAssertion{}, false
	}
}

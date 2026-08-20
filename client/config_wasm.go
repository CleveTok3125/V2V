//go:build js

package main

import (
	"syscall/js"
)

// parseFlags reads the connection config that the web page placed in
// window.v2vConfig. Guest-only: no key file / role is ever used on the web.
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

	if CLI.Server == "" {
		CLI.Server = js.Global().Get("location").Get("origin").String() + "/"
	}
	if CLI.Username == "" {
		CLI.Username = "Anonymous"
	}
}
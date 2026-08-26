//go:build !js

package main

import "github.com/alecthomas/kong"

func parseFlags() {
	kong.Parse(&CLI, kong.Vars{
		"version": Version,
	})
}

// requestAssertion is web-only: the desktop signs assertions natively via
// PasskeyIdentity.BuildAssertion, so the browser bridge never runs here.
func requestAssertion(string, string) (webauthnAssertion, bool) {
	return webauthnAssertion{}, false
}

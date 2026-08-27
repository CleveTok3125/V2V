//go:build !js

package main

import "github.com/alecthomas/kong"

func parseFlags() {
	kong.Parse(&CLI, kong.Vars{
		"version": Version,
	})
}

// applyWebPasskey is web-only: the desktop signs assertions natively from
// its -k identity file, so this path never engages here. Returning true
// simply means "nothing failed; continue as configured".
func applyWebPasskey(*AuthPacket, string) bool {
	return true
}

func setWasmStatus(string, bool) bool { return false }

func showWasmStatus(string, bool) bool { return false }

func parkForever() {}

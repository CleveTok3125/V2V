//go:build !js

package main

import "github.com/alecthomas/kong"

func parseFlags() {
	kong.Parse(&CLI, kong.Vars{
		"version": Version,
	})
}
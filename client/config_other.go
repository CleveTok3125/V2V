//go:build !js

package main

import (
	"path/filepath"

	"github.com/alecthomas/kong"

	"github.com/CleveTok3125/V2V/internal/config"
	"github.com/CleveTok3125/V2V/internal/configdir"
)

var ClientCfg *config.ClientConfig

func parseFlags() {
	kong.Parse(&CLI, kong.Vars{
		"version": Version,
	})
	if CLI.ConfigDir == "" {
		CLI.ConfigDir = configdir.DefaultConfigDir()
	}
	if CLI.CacheDir == "" {
		CLI.CacheDir = configdir.DefaultCacheDir()
	}
	if CLI.KeyFile != "" {
		// Explicit path wins (breaks old -k <path>, now -K/--key-file)
	} else if CLI.UseKey {
		CLI.KeyFile = filepath.Join(CLI.ConfigDir, "key.json")
	} else {
		CLI.KeyFile = ""
	}
	historyFile = filepath.Join(CLI.CacheDir, "history.tmp")
	// Load or auto-create client config.json
	cfgPath := configdir.DefaultConfigFile(CLI.ConfigDir)
	if cfg, err := config.LoadOrCreate(cfgPath, true); err == nil {
		ClientCfg = cfg
	} else {
		ClientCfg = config.DefaultClientConfig()
	}
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

//go:build !js

package main

import (
	"fmt"
	"os"
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
	// Client config is immutable state: read freely, replaced only by
	// explicit actions. A missing file means in-memory defaults; copy
	// template/config.json to config.jsonc to customize.
	cfgPath := resolveCfgPath()
	if cfg, err := config.Load(cfgPath); err == nil {
		ClientCfg = cfg
	} else {
		ClientCfg = config.DefaultClientConfig()
	}
}

// resolveCfgPath prefers config.jsonc, falls back to config.json.
func resolveCfgPath() string {
	cfgPath := configdir.DefaultConfigFile(CLI.ConfigDir)
	jsoncPath := cfgPath[:len(cfgPath)-len(".json")] + ".jsonc"
	if _, err := os.Stat(jsoncPath); err == nil {
		return jsoncPath
	}
	if _, err := os.Stat(cfgPath); err != nil {
		fmt.Printf("config %s not found, using defaults (see template/config.json)\n", cfgPath)
	}
	return cfgPath
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

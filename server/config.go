package main

import (
	"path/filepath"
	"sync/atomic"
	"time"

	"github.com/CleveTok3125/V2V/internal/config"
	"github.com/CleveTok3125/V2V/internal/env"
)

type StaticConfig struct {
	Port                 string
	RequireTLS           bool
	AllowedOrigins       []string
	InstanceID           string
	Timezone             *time.Location
	LogFilePath          string
	MaxLogSizeMB         int
	HistoryFilePath      string
	MaxHistoryFileSizeMB int
	// ProxyChain is the explicit reverse-proxy chain (e.g.
	// ["cloudflare", "direct"]). Parsed from PROXY_PROVIDER at
	// boot; empty is fatal, there is no implicit default.
	ProxyChain []string
	// TrustedProxyDir holds per-module "<name>.txt" trust files.
	TrustedProxyDir string
}

type DynamicConfig = config.DynamicConfig

type AppConfig struct {
	Static  StaticConfig
	Dynamic atomic.Pointer[DynamicConfig]
}

var Cfg AppConfig

var (
	// Live admin config lives in config/. No fallbacks: an unmigrated
	// deploy fails closed on required vars instead of booting on
	// defaults.
	EnvFilePaths   = []string{".env"}
	RolesFilePaths = []string{"config/roles.json"}
)

// dataPath resolves a generated-file name under DATA_DIR (default
// ./data). Explicit per-file env still wins at the call site.
func dataPath(name string) string {
	dir := env.DataDir()
	if dir == "" {
		dir = "./data"
	}
	return filepath.Join(dir, name)
}

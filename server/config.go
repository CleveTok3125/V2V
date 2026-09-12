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
	EnvFilePaths   = []string{"config/.env"}
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

package main

import (
	"sync/atomic"
	"time"

	"github.com/CleveTok3125/V2V/internal/config"
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
	EnvFilePaths   = []string{".env"}
	RolesFilePaths = []string{"./roles.json"}
)

package main

import (
	"sync/atomic"
	"time"
)

type StaticConfig struct {
	Port           string
	RequireTLS     bool
	AllowedOrigins []string
	InstanceID     string
	Timezone       *time.Location
	LogFilePath    string
	MaxLogSizeMB   int
}

type DynamicConfig struct {
	StatusURL   string
	DownloadURL string
	HomepageURL string

	MaxConnectionsPerIP int
	MaxMessageLength    int
	MaxMessageLine      int
	MessageCooldown     time.Duration
	MaxHistoryBytes     int
	MaxHistorySend      int
	MaxUsernameLength   int
	MaxTripcodeLength   int
	ConnectionCooldown  time.Duration
}

type AppConfig struct {
	Static  StaticConfig
	Dynamic atomic.Pointer[DynamicConfig]
}

var Cfg AppConfig

var (
	EnvFilePaths   = []string{".env"}
	RolesFilePaths = []string{"./roles.json"}
)

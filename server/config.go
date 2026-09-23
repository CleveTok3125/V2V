package main

import (
	"path/filepath"
	"strings"
	"sync/atomic"

	"github.com/CleveTok3125/V2V/internal/config"
	"github.com/CleveTok3125/V2V/internal/env"
	"github.com/CleveTok3125/V2V/internal/serverconfig"
)

// StaticConfig and OnionConfig are aliases of the shared loader types so
// the server and v2vctl config validate parse the same shape.
type StaticConfig = serverconfig.StaticConfig

type OnionConfig = serverconfig.OnionConfig

type DynamicConfig = config.DynamicConfig

type AppConfig struct {
	Static  StaticConfig
	Dynamic atomic.Pointer[DynamicConfig]
}

var Cfg AppConfig

// DefaultTrustedProxyDir is the conventional trust-file directory,
// mirroring config/roles.json.
const DefaultTrustedProxyDir = serverconfig.DefaultTrustedProxyDir

// ServerRoot is the instance directory this process operates on,
// resolved from V2V_ROOT at boot (default instances/default). Relative
// config, data and log defaults hang off it, so the same binary serves
// different instances by changing the root.
var ServerRoot string

// EnvFilePaths and RolesFilePaths are resolved under ServerRoot. Tests
// override them directly.
var (
	EnvFilePaths   []string
	RolesFilePaths []string
)

// DefaultServerRoot is V2V_ROOT when set, else instances/default.
func DefaultServerRoot() string {
	if r := strings.TrimSpace(env.Root()); r != "" {
		return r
	}
	return filepath.Join("instances", "default")
}

// InitServerPaths points the process at one instance root. Must run
// before the .env load so EnvFilePaths locates <root>/.env.
func InitServerPaths(root string) {
	ServerRoot = root
	EnvFilePaths = []string{filepath.Join(root, ".env")}
	RolesFilePaths = []string{filepath.Join(root, "config", "roles.json")}
}

func init() { InitServerPaths(DefaultServerRoot()) }

// resolveUnderRoot keeps absolute overrides untouched and anchors relative
// ones at the instance root.
func resolveUnderRoot(root, p string) string { return serverconfig.ResolveUnderRoot(root, p) }

// dataPath resolves a generated-file name under DATA_DIR or <root>/data.
func dataPath(name string) string { return serverconfig.DataPath(ServerRoot, name) }

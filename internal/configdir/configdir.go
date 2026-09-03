package configdir

import (
	"os"
	"path/filepath"
)

// DefaultConfigDir returns the standard config directory for V2V:
// linux: $XDG_CONFIG_HOME/V2V or ~/.config/V2V
// windows: %AppData%/V2V
// darwin: ~/Library/Application Support/V2V
// android: same as linux, with fallback to /sdcard/.config/V2V when HOME empty
func DefaultConfigDir() string {
	if d, err := os.UserConfigDir(); err == nil && d != "" {
		return filepath.Join(d, "V2V")
	}
	if h, err := os.UserHomeDir(); err == nil && h != "" {
		return filepath.Join(h, ".config", "V2V")
	}
	return "."
}

// DefaultCacheDir returns the standard cache directory for V2V:
// linux: $XDG_CACHE_HOME/V2V or ~/.cache/V2V
// windows: %LocalAppData%/V2V
// darwin: ~/Library/Caches/V2V
func DefaultCacheDir() string {
	if d, err := os.UserCacheDir(); err == nil && d != "" {
		return filepath.Join(d, "V2V")
	}
	if h, err := os.UserHomeDir(); err == nil && h != "" {
		return filepath.Join(h, ".cache", "V2V")
	}
	return os.TempDir()
}

// DefaultKeyFile returns the default key.json path inside config dir.
func DefaultKeyFile(configDir string) string {
	if configDir == "" {
		configDir = DefaultConfigDir()
	}
	return filepath.Join(configDir, "key.json")
}

// DefaultHistoryFile returns the default history file path inside cache dir.
func DefaultHistoryFile(cacheDir string) string {
	if cacheDir == "" {
		cacheDir = DefaultCacheDir()
	}
	return filepath.Join(cacheDir, "history.tmp")
}

// DefaultConfigFile returns the default config.json path (for future use).
func DefaultConfigFile(configDir string) string {
	if configDir == "" {
		configDir = DefaultConfigDir()
	}
	return filepath.Join(configDir, "config.json")
}

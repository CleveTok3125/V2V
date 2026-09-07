package configdir

import (
	"path/filepath"
	"strings"
	"testing"
)

// Default dirs must resolve without action: XDG first, then OS cache,
// then home, then temp. The test pins shape, not exact paths.
func TestDefaultDirs_Shape(t *testing.T) {
	cfg := DefaultConfigDir()
	if cfg == "" || !filepath.IsAbs(cfg) {
		t.Fatalf("bad config dir: %q", cfg)
	}
	cache := DefaultCacheDir()
	if cache == "" || !filepath.IsAbs(cache) {
		t.Fatalf("bad cache dir: %q", cache)
	}
	t.Setenv("XDG_CONFIG_HOME", "/tmp/xdg-cfg")
	t.Setenv("XDG_CACHE_HOME", "/tmp/xdg-cache")
	if got := DefaultConfigDir(); got != filepath.Join("/tmp/xdg-cfg", "V2V") {
		t.Fatalf("XDG config not honored: %q", got)
	}
	if got := DefaultCacheDir(); got != filepath.Join("/tmp/xdg-cache", "V2V") {
		t.Fatalf("XDG cache not honored: %q", got)
	}
	if f := DefaultConfigFile(""); !strings.HasSuffix(f, ".json") {
		t.Fatalf("config file shape: %q", f)
	}
}

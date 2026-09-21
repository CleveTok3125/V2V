package main

import (
	"fmt"
	"net"
	"path/filepath"
	"strings"
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
	// Onion holds Tor hidden-service ingress policy. Hosts is the set of
	// accepted "<id>.onion" names; a request to one of them from a local or
	// direct-trusted hop is treated as already encrypted (Tor provides
	// end-to-end crypto) and skips the TLS requirement.
	Onion OnionConfig
	// ProxyChain is the explicit reverse-proxy chain (e.g.
	// ["cloudflare", "direct"]). Parsed from PROXY_PROVIDER at
	// boot; empty is fatal, there is no implicit default.
	ProxyChain []string
	// TrustedProxyDir holds per-module "<name>.txt" trust files.
	TrustedProxyDir string
}

// OnionConfig gates Tor hidden-service ingress. Feature flags are only
// restrictions: when a master feature (WebAuthn, the web client) is off,
// flipping these on cannot resurrect it.
type OnionConfig struct {
	Hosts        []string
	AllowWeb     bool
	AllowPasskey bool
	// Trust lists tor/NAT hop addresses allowed to present an onion Host
	// without TLS. Runtime-only, loaded from config/trustedproxy/onion.txt.
	Trust []*net.IPNet
}

// OnionEnabled reports whether any onion host is configured.
func (c *StaticConfig) OnionEnabled() bool {
	return len(c.Onion.Hosts) > 0
}

// IsOnionHost reports whether host (case-insensitive, optional port) is a
// configured onion host.
func (c *StaticConfig) IsOnionHost(host string) bool {
	if len(c.Onion.Hosts) == 0 {
		return false
	}
	name := normalizeOnionHost(host)
	if name == "" {
		return false
	}
	for _, h := range c.Onion.Hosts {
		if h == name {
			return true
		}
	}
	return false
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

// normalizeOnionHost lowercases a Host header value, strips an optional
// port and a trailing FQDN dot, leaving a bare hostname. Empty means
// unusable.
func normalizeOnionHost(host string) string {
	host = strings.TrimSpace(strings.ToLower(host))
	if host == "" {
		return ""
	}
	if h, _, err := net.SplitHostPort(host); err == nil {
		return h
	}
	return strings.TrimSuffix(strings.Trim(host, "[]"), ".")
}

// parseOnionHosts parses the comma-separated ONION_HOSTS list into
// normalized hostnames, dropping empties and duplicates. Every entry must
// end in ".onion": a non-onion value would let clearnet traffic bypass the
// TLS requirement, so it is a boot error instead.
func parseOnionHosts(raw string) ([]string, error) {
	var out []string
	seen := map[string]bool{}
	for _, part := range strings.Split(raw, ",") {
		name := normalizeOnionHost(part)
		if name == "" {
			continue
		}
		if !strings.HasSuffix(name, ".onion") {
			return nil, fmt.Errorf("ONION_HOSTS entry %q is not a .onion host", part)
		}
		if seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	return out, nil
}

// dataPath resolves a generated-file name under DATA_DIR (default
// ./data). Explicit per-file env still wins at the call site.
func dataPath(name string) string {
	dir := env.DataDir()
	if dir == "" {
		dir = "./data"
	}
	return filepath.Join(dir, name)
}

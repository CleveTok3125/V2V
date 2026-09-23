// Package serverconfig is the single source of truth for loading and
// validating the V2V server configuration. Both the server binary and
// the v2vctl config validate command use it, so a setting can never be
// parsed differently in the two places.
//
// The package never writes to a logger: every loader returns warnings as
// strings and leaves it to the caller to log them. Fatal problems are
// returned as errors.
package serverconfig

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/CleveTok3125/V2V/internal/env"
)

// DefaultTrustedProxyDir is the conventional trust-file directory,
// mirroring config/roles.json. Only the listed chain members load
// their "<name>.txt" from here.
const DefaultTrustedProxyDir = "config/trustedproxy"

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
	// WebEnabled is the master switch for the WASM web client served at
	// /web/. False denies it for every request; onion requests are
	// additionally gated by Onion.AllowWeb.
	WebEnabled bool
	// Root is the instance directory (V2V_ROOT, default instances/default)
	// that .env, config/ and data/ hang off.
	Root string
	// NoContentLogs is the content-privacy policy: chat history stays in
	// RAM only and message content is never logged. Operational metadata
	// (client IP, auth/identity events, admin and error lines) is still
	// logged, and identity/auth files are still written. This is NOT
	// "zero logs".
	NoContentLogs bool
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

// ParseOnionHosts parses the comma-separated ONION_HOSTS list into
// normalized hostnames, dropping empties and duplicates. Every entry must
// end in ".onion": a non-onion value would let clearnet traffic bypass the
// TLS requirement, so it is a boot error instead.
func ParseOnionHosts(raw string) ([]string, error) {
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

// ResolveUnderRoot keeps absolute overrides untouched and anchors relative
// ones at the instance root, so one .env works for any instance and a
// relative default such as ./data/app.log never escapes to the cwd.
func ResolveUnderRoot(root, p string) string {
	p = strings.TrimSpace(p)
	if p == "" || filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(root, p)
}

// DataPath resolves a generated-file name under DATA_DIR (an explicit
// override, absolute or root-relative) or <root>/data by default.
func DataPath(root, name string) string {
	if dir := strings.TrimSpace(env.DataDir()); dir != "" {
		return filepath.Join(ResolveUnderRoot(root, dir), name)
	}
	return filepath.Join(root, "data", name)
}

// EffectiveStoragePaths applies the NO_CONTENT_LOGS policy: content paths
// are cleared so the logger and history store stay off-disk.
func EffectiveStoragePaths(noContentLogs bool, logPath, historyPath string) (string, string) {
	if noContentLogs {
		return "", ""
	}
	return logPath, historyPath
}

// WarnStaleContentFiles reports pre-existing history/log artifacts that
// NO_CONTENT_LOGS leaves on disk. It never deletes: removal stays an
// operator action. The caller logs the returned lines.
func WarnStaleContentFiles(logPath, historyPath string) []string {
	var out []string
	for _, p := range []string{
		historyPath, historyPath + ".old", historyPath + ".old.zst",
		logPath, logPath + ".old",
	} {
		if p == "" {
			continue
		}
		if _, err := os.Stat(p); err == nil {
			out = append(out, fmt.Sprintf("⚠️ NO_CONTENT_LOGS: còn file %s trên đĩa; hãy tự xoá nếu muốn sạch dấu vết", p))
		}
	}
	return out
}

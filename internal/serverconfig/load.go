package serverconfig

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/CleveTok3125/V2V/internal/config"
	"github.com/CleveTok3125/V2V/internal/env"
	"github.com/CleveTok3125/V2V/internal/trustedproxy"
)

// History disk-lookup tiers mirrored from the server history store:
// 0 is RAM-only, 3 is the full archive. Out-of-range values clamp to 0.
const (
	diskLookupOff     = 0
	diskLookupArchive = 3
)

// ProxyEntry is one provider's trust-file resolution for boot reporting.
type ProxyEntry struct {
	Name    string
	Path    string
	Count   int
	Missing bool
}

// ProxyChainInfo carries everything needed to build the active chain and
// print the boot report. The caller logs, this package does not.
type ProxyChainInfo struct {
	Chain        *trustedproxy.Chain
	Names        []string
	Entries      []ProxyEntry
	AllowsDirect bool
}

// LoadStaticConfig reads the static configuration for one instance root.
// It returns collected warnings even when an error is returned, so the
// caller can log them before failing. The root is a parameter rather than
// a global: the same binary serves different instances by changing it.
func LoadStaticConfig(root string) (StaticConfig, []string, error) {
	var w warnings
	loader := &EnvLoader{}
	rawInstanceID := getEnvFallback("INSTANCE_ID", "AUTO")
	var instanceID string
	if rawInstanceID == "AUTO" {
		instanceID = generateRandomID(6)
	} else {
		instanceID = lastAfterDash(loader.Smart("INSTANCE_ID"))
	}

	onionHosts, err := ParseOnionHosts(getEnvFallback("ONION_HOSTS", ""))
	if err != nil {
		return StaticConfig{}, w.list, err
	}

	noContentLogs := getEnvAsBoolFallback(&w, "NO_CONTENT_LOGS", false)
	logFilePath := DataPath(root, "app.log")
	if raw := os.Getenv("LOG_FILE_PATH"); strings.TrimSpace(raw) != "" {
		logFilePath = ResolveUnderRoot(root, raw)
	}
	historyFilePath := DataPath(root, "history.jsonl")
	if raw := os.Getenv("HISTORY_FILE_PATH"); strings.TrimSpace(raw) != "" {
		historyFilePath = ResolveUnderRoot(root, raw)
	}
	if noContentLogs {
		if strings.TrimSpace(os.Getenv("LOG_FILE_PATH")) != "" || strings.TrimSpace(os.Getenv("HISTORY_FILE_PATH")) != "" {
			w.addf("⚠️ NO_CONTENT_LOGS=true: LOG_FILE_PATH/HISTORY_FILE_PATH bị bỏ qua (RAM-only)")
		}
		w.list = append(w.list, WarnStaleContentFiles(logFilePath, historyFilePath)...)
	}
	logFilePath, historyFilePath = EffectiveStoragePaths(noContentLogs, logFilePath, historyFilePath)

	trustedProxyDir := filepath.Join(root, DefaultTrustedProxyDir)
	if raw := os.Getenv(env.KeyTrustedProxyDir); strings.TrimSpace(raw) != "" {
		trustedProxyDir = ResolveUnderRoot(root, raw)
	}

	cfg := StaticConfig{
		AllowedOrigins:       strings.Split(env.AllowedOrigins(), ","),
		RequireTLS:           getEnvAsBoolFallback(&w, "REQUIRE_TLS", true),
		Port:                 loader.Smart("PORT"),
		InstanceID:           instanceID,
		Timezone:             getEnvAsLocationFallback(&w, "TIMEZONE", "Asia/Ho_Chi_Minh"),
		LogFilePath:          logFilePath,
		MaxLogSizeMB:         loader.Int("MAX_LOG_SIZE_MB"),
		HistoryFilePath:      historyFilePath,
		MaxHistoryFileSizeMB: loader.Int("MAX_HISTORY_FILE_SIZE_MB"),
		NoContentLogs:        noContentLogs,
		Root:                 root,
		TrustedProxyDir:      trustedProxyDir,
		WebEnabled:           getEnvAsBoolFallback(&w, "WEB_ENABLED", true),
		Onion: OnionConfig{
			Hosts:        onionHosts,
			AllowWeb:     getEnvAsBoolFallback(&w, "ONION_ALLOW_WEB", false),
			AllowPasskey: getEnvAsBoolFallback(&w, "ONION_ALLOW_PASSKEY", false),
		},
	}
	if err := loader.Err(); err != nil {
		return StaticConfig{}, w.list, err
	}
	// PROXY_PROVIDER is required with no implicit default: the
	// operator must state the proxy chain explicitly (fail-closed).
	chain, err := trustedproxy.ParseChain(loader.Smart(env.KeyProxyProvider))
	if err != nil {
		return StaticConfig{}, w.list, err
	}
	cfg.ProxyChain = chain

	return cfg, w.list, nil
}

// LoadDynamicConfig reads the hot-reloadable configuration. It returns
// collected warnings alongside the config.
func LoadDynamicConfig() (config.DynamicConfig, []string, error) {
	var w warnings
	loader := &EnvLoader{}

	cfg := config.DynamicConfig{
		StatusURL:              loader.Optional("STATUS_URL"),
		DownloadURL:            loader.Optional("DOWNLOAD_URL"),
		HomepageURL:            loader.Optional("HOMEPAGE_URL"),
		MaxConnectionsPerIP:    loader.Int("MAX_CONNECTIONS_PER_IP"),
		MaxMessageLength:       loader.Int("MAX_MESSAGE_LENGTH"),
		MaxMessageLine:         loader.Int("MAX_MESSAGE_LINE"),
		MessageCooldown:        loader.Duration("MESSAGE_COOLDOWN"),
		IdleChatTimeout:        loader.Duration("IDLE_CHAT_TIMEOUT"),
		MaxHistoryBytes:        loader.Int("MAX_HISTORY_BYTES"),
		MaxHistorySend:         loader.Int("MAX_HISTORY_SEND"),
		HistorySegmentCooldown: loader.Duration("HISTORY_SEGMENT_COOLDOWN"),
		HistoryDiskLookup:      loader.Int("HISTORY_DISK_LOOKUP"),
		MaxUsernameLength:      loader.Int("MAX_USERNAME_LENGTH"),
		MaxTripcodeLength:      getEnvAsIntFallback(&w, "MAX_TRIPCODE_LENGTH", 64),
		ConnectionCooldown:     loader.Duration("CONNECTION_COOLDOWN"),
	}
	if err := loader.Err(); err != nil {
		return config.DynamicConfig{}, w.list, err
	}
	// Fail-safe floor: a zero/negative replay window would silently send
	// empty history on every connect. Mirror the client backfill default.
	if cfg.MaxHistorySend <= 0 {
		cfg.MaxHistorySend = 500
	}
	// Fail-closed floors: a missing/zero segment throttle would let one
	// client re-scan history unthrottled; an out-of-range disk tier must
	// never widen reads beyond what the operator picked.
	if cfg.HistorySegmentCooldown <= 0 {
		cfg.HistorySegmentCooldown = 2 * time.Second
	}
	if cfg.HistoryDiskLookup < diskLookupOff || cfg.HistoryDiskLookup > diskLookupArchive {
		cfg.HistoryDiskLookup = diskLookupOff
	}

	return cfg, w.list, nil
}

// LoadProxyChain loads per-module trust files and builds the resolution
// chain. Errors are fatal to the caller: trust must never silently
// degrade. The returned info carries the boot report data; warnings hold
// the aggregated stray-file notice.
func LoadProxyChain(dir string, names []string) (*ProxyChainInfo, []string, error) {
	required := map[string]bool{}
	for _, name := range names {
		if build, ok := trustedproxy.Lookup(name); ok && build(nil).NeedsFile() {
			required[name] = true
		}
	}
	sets, extraWarn, err := trustedproxy.LoadTrustDir(dir, names, required)
	if err != nil {
		return nil, nil, err
	}
	nets := make(map[string][]*net.IPNet, len(sets))
	for name, set := range sets {
		nets[name] = set.Nets
	}
	chain, err := trustedproxy.NewChain(names, nets)
	if err != nil {
		return nil, nil, err
	}
	entries := make([]ProxyEntry, 0, len(names))
	allowsDirect := false
	for _, name := range names {
		set := sets[name]
		entries = append(entries, ProxyEntry{
			Name:    name,
			Path:    set.Path,
			Count:   len(set.Nets),
			Missing: set.Missing,
		})
		if name == "none" || name == "direct" {
			allowsDirect = true
		}
	}
	var warns []string
	if extraWarn != "" {
		warns = append(warns, fmt.Sprintf("⚠️ Trusted proxy: %s", extraWarn))
	}
	return &ProxyChainInfo{
		Chain:        chain,
		Names:        names,
		Entries:      entries,
		AllowsDirect: allowsDirect,
	}, warns, nil
}

// LoadOnionTrust loads the onion-hop allowlist from onion.txt next to the
// other trust files. A missing file means loopback-only and is returned as
// a warning with nil nets; a malformed or world-writable file fails.
func LoadOnionTrust(dir string) ([]*net.IPNet, []string, error) {
	path := filepath.Join(dir, "onion.txt")
	set, err := trustedproxy.LoadTrustFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, []string{fmt.Sprintf("⚠️ Onion: %s missing; only loopback hops accepted", path)}, nil
		}
		return nil, nil, err
	}
	return set.Nets, nil, nil
}

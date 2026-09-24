package serverconfig

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// setStaticEnv pins every static variable LoadStaticConfig reads to a known
// value, so ambient environment noise never leaks into the test.
func setStaticEnv(t *testing.T) {
	t.Helper()
	t.Setenv("PORT", "10000")
	t.Setenv("MAX_LOG_SIZE_MB", "50")
	t.Setenv("MAX_HISTORY_FILE_SIZE_MB", "50")
	t.Setenv("PROXY_PROVIDER", "none")
	t.Setenv("INSTANCE_ID", "test")
	t.Setenv("ONION_HOSTS", "")
	t.Setenv("NO_CONTENT_LOGS", "false")
	t.Setenv("LOG_FILE_PATH", "")
	t.Setenv("HISTORY_FILE_PATH", "")
	t.Setenv("TRUSTED_PROXY_DIR", "")
	t.Setenv("ALLOWED_ORIGINS", "")
	t.Setenv("REQUIRE_TLS", "true")
	t.Setenv("TIMEZONE", "Asia/Ho_Chi_Minh")
	t.Setenv("WEB_ENABLED", "true")
	t.Setenv("ONION_ALLOW_WEB", "")
	t.Setenv("ONION_ALLOW_PASSKEY", "")
	t.Setenv("DATA_DIR", "")
	t.Setenv("BEHAVIOR_ENABLED", "true")
	t.Setenv("BEHAVIOR_GEOIP_DIR", "./config/geoip")
	t.Setenv("BEHAVIOR_FILE_PATH", "./data/behavior.jsonl")
	t.Setenv("REQUIRE_IPV4", "false")
	t.Setenv("BLOCKLIST_FILE", "./config/blocklist.txt")
}

// setDynamicEnv pins every dynamic variable LoadDynamicConfig reads.
func setDynamicEnv(t *testing.T) {
	t.Helper()
	t.Setenv("STATUS_URL", "")
	t.Setenv("DOWNLOAD_URL", "")
	t.Setenv("HOMEPAGE_URL", "")
	t.Setenv("MAX_CONNECTIONS_PER_IP", "2")
	t.Setenv("MAX_MESSAGE_LENGTH", "5000")
	t.Setenv("MAX_MESSAGE_LINE", "50")
	t.Setenv("MESSAGE_COOLDOWN", "200ms")
	t.Setenv("IDLE_CHAT_TIMEOUT", "30m")
	t.Setenv("MAX_HISTORY_BYTES", "10485760")
	t.Setenv("MAX_HISTORY_SEND", "500")
	t.Setenv("HISTORY_SEGMENT_COOLDOWN", "2s")
	t.Setenv("HISTORY_DISK_LOOKUP", "0")
	t.Setenv("MAX_USERNAME_LENGTH", "12")
	t.Setenv("MAX_TRIPCODE_LENGTH", "64")
	t.Setenv("CONNECTION_COOLDOWN", "5s")
}

func TestLoadStaticConfigHappyPath(t *testing.T) {
	setStaticEnv(t)
	root := t.TempDir()
	cfg, warns, err := LoadStaticConfig(root)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(warns) != 0 {
		t.Fatalf("clean config must not warn: %v", warns)
	}
	if cfg.Root != root {
		t.Fatalf("root = %q, want %q", cfg.Root, root)
	}
	if cfg.Port != "10000" || !cfg.RequireTLS || !cfg.WebEnabled {
		t.Fatalf("basic fields wrong: %+v", cfg)
	}
	if len(cfg.ProxyChain) != 1 || cfg.ProxyChain[0] != "none" {
		t.Fatalf("proxy chain = %v", cfg.ProxyChain)
	}
	if cfg.TrustedProxyDir != filepath.Join(root, DefaultTrustedProxyDir) {
		t.Fatalf("trusted proxy dir = %q", cfg.TrustedProxyDir)
	}
	if cfg.LogFilePath != filepath.Join(root, "data", "app.log") {
		t.Fatalf("log path = %q", cfg.LogFilePath)
	}
}

func TestLoadStaticConfigRequiresProxyProvider(t *testing.T) {
	setStaticEnv(t)
	t.Setenv("PROXY_PROVIDER", "")
	if _, _, err := LoadStaticConfig(t.TempDir()); err == nil {
		t.Fatal("missing PROXY_PROVIDER must fail")
	}
	t.Setenv("PROXY_PROVIDER", "false")
	if _, _, err := LoadStaticConfig(t.TempDir()); err == nil {
		t.Fatal("PROXY_PROVIDER=false must fail")
	}
}

func TestLoadStaticConfigRejectsNonOnionHost(t *testing.T) {
	setStaticEnv(t)
	t.Setenv("ONION_HOSTS", "example.com")
	if _, _, err := LoadStaticConfig(t.TempDir()); err == nil {
		t.Fatal("non-.onion ONION_HOSTS entry must fail")
	}
	t.Setenv("ONION_HOSTS", "a.onion,evil.com")
	if _, _, err := LoadStaticConfig(t.TempDir()); err == nil {
		t.Fatal("mixed ONION_HOSTS list must fail")
	}
}

func TestLoadStaticConfigBoolFallbackWarns(t *testing.T) {
	setStaticEnv(t)
	t.Setenv("WEB_ENABLED", "not-a-bool")
	cfg, warns, err := LoadStaticConfig(t.TempDir())
	if err != nil {
		t.Fatalf("malformed bool must fall back, not fail: %v", err)
	}
	if !cfg.WebEnabled {
		t.Fatal("malformed WEB_ENABLED must fall back to true")
	}
	if len(warns) == 0 || !strings.Contains(warns[0], "WEB_ENABLED") {
		t.Fatalf("expected a WEB_ENABLED warning, got %v", warns)
	}
}

func TestLoadStaticConfigTimezoneFallbackWarns(t *testing.T) {
	setStaticEnv(t)
	t.Setenv("TIMEZONE", "Not/AZone")
	cfg, warns, err := LoadStaticConfig(t.TempDir())
	if err != nil {
		t.Fatalf("bad timezone must fall back: %v", err)
	}
	if cfg.Timezone != time.Local {
		t.Fatalf("timezone = %v, want Local", cfg.Timezone)
	}
	if len(warns) == 0 {
		t.Fatal("expected a timezone warning")
	}
}

func TestLoadDynamicConfigMissingURLsOK(t *testing.T) {
	setDynamicEnv(t)
	cfg, warns, err := LoadDynamicConfig()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(warns) != 0 {
		t.Fatalf("clean dynamic config must not warn: %v", warns)
	}
	if cfg.StatusURL != "" || cfg.DownloadURL != "" || cfg.HomepageURL != "" {
		t.Fatalf("absent URLs must be empty: %+v", cfg)
	}
	if cfg.MaxMessageLength != 5000 || cfg.MaxHistorySend != 500 {
		t.Fatalf("numeric fields wrong: %+v", cfg)
	}
}

func TestLoadDynamicConfigRejectsBadInt(t *testing.T) {
	setDynamicEnv(t)
	t.Setenv("MAX_MESSAGE_LENGTH", "abc")
	if _, _, err := LoadDynamicConfig(); err == nil {
		t.Fatal("non-numeric MAX_MESSAGE_LENGTH must fail")
	}
}

func TestLoadDynamicConfigTripcodeFallbackWarns(t *testing.T) {
	setDynamicEnv(t)
	t.Setenv("MAX_TRIPCODE_LENGTH", "abc")
	cfg, warns, err := LoadDynamicConfig()
	if err != nil {
		t.Fatalf("bad fallback int must not fail: %v", err)
	}
	if cfg.MaxTripcodeLength != 64 {
		t.Fatalf("tripcode fallback = %d, want 64", cfg.MaxTripcodeLength)
	}
	if len(warns) == 0 || !strings.Contains(warns[0], "MAX_TRIPCODE_LENGTH") {
		t.Fatalf("expected a MAX_TRIPCODE_LENGTH warning, got %v", warns)
	}
}

func TestLoadDynamicConfigClampsDiskLookup(t *testing.T) {
	setDynamicEnv(t)
	t.Setenv("HISTORY_DISK_LOOKUP", "99")
	cfg, _, err := LoadDynamicConfig()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.HistoryDiskLookup != DiskLookupOff {
		t.Fatalf("out-of-range tier = %d, want %d", cfg.HistoryDiskLookup, DiskLookupOff)
	}
}

func TestLoadProxyChainAndTrust(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "cloudflare.txt"), []byte("173.245.48.0/20\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	info, warns, err := LoadProxyChain(dir, []string{"cloudflare", "direct"})
	if err != nil {
		t.Fatalf("load chain: %v", err)
	}
	if len(warns) != 0 {
		t.Fatalf("clean chain must not warn: %v", warns)
	}
	if info.Chain == nil || len(info.Entries) != 2 || info.AllowsDirect != true {
		t.Fatalf("chain info wrong: %+v", info)
	}
	if info.Entries[0].Name != "cloudflare" || info.Entries[0].Count != 1 || info.Entries[0].Missing {
		t.Fatalf("cloudflare entry wrong: %+v", info.Entries[0])
	}
	if !info.Entries[1].Missing {
		t.Fatalf("direct entry must report a missing optional file: %+v", info.Entries[1])
	}
}

func TestLoadProxyChainMissingRequiredFileFails(t *testing.T) {
	if _, _, err := LoadProxyChain(t.TempDir(), []string{"cloudflare"}); err == nil {
		t.Fatal("missing cloudflare.txt must fail closed")
	}
}

func TestLoadProxyChainAbsentDirWarns(t *testing.T) {
	info, warns, err := LoadProxyChain(filepath.Join(t.TempDir(), "nope"), []string{"none"})
	if err != nil {
		t.Fatalf("file-less none chain must boot: %v", err)
	}
	if info.Chain == nil || info.Entries[0].Missing != true {
		t.Fatalf("none must resolve without a file: %+v", info)
	}
	if len(warns) == 0 {
		t.Fatal("absent trust dir must warn")
	}
}

func TestLoadOnionTrust(t *testing.T) {
	dir := t.TempDir()
	nets, warns, err := LoadOnionTrust(dir)
	if err != nil || nets != nil {
		t.Fatalf("missing onion.txt must be tolerated: %v %v", nets, err)
	}
	if len(warns) == 0 {
		t.Fatal("missing onion.txt must warn")
	}

	path := filepath.Join(dir, "onion.txt")
	if err := os.WriteFile(path, []byte("10.0.0.5/32\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	nets, warns, err = LoadOnionTrust(dir)
	if err != nil || len(nets) != 1 || len(warns) != 0 {
		t.Fatalf("valid onion.txt: nets=%v warns=%v err=%v", nets, warns, err)
	}
	if !nets[0].Contains(net.ParseIP("10.0.0.5")) {
		t.Fatal("loaded net does not contain the configured hop")
	}

	if err := os.WriteFile(path, []byte("not-an-ip\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := LoadOnionTrust(dir); err == nil {
		t.Fatal("malformed onion.txt must fail")
	}
}

func TestParseOnionHosts(t *testing.T) {
	got, err := ParseOnionHosts(" AbCd.Onion , dup.onion, ABCD.onion:8080, , trail.onion.")
	if err != nil {
		t.Fatalf("valid list must parse: %v", err)
	}
	want := []string{"abcd.onion", "dup.onion", "trail.onion"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
	if out, err := ParseOnionHosts("  "); err != nil || len(out) != 0 {
		t.Fatalf("blank list must be empty: %v %v", out, err)
	}
}

func TestOnionHostMatching(t *testing.T) {
	cfg := &StaticConfig{Onion: OnionConfig{Hosts: []string{"abcd.onion"}}}
	if !cfg.OnionEnabled() {
		t.Fatal("configured host must enable onion")
	}
	for _, host := range []string{"abcd.onion", "ABCD.ONION", "abcd.onion:80", "abcd.onion."} {
		if !cfg.IsOnionHost(host) {
			t.Fatalf("host %q must match", host)
		}
	}
	for _, host := range []string{"", "other.onion", "abcd.onion.evil.com"} {
		if cfg.IsOnionHost(host) {
			t.Fatalf("host %q must not match", host)
		}
	}
	empty := &StaticConfig{}
	if empty.OnionEnabled() || empty.IsOnionHost("abcd.onion") {
		t.Fatal("no hosts configured means onion disabled")
	}
}

func TestEffectiveStoragePaths(t *testing.T) {
	if l, h := EffectiveStoragePaths(false, "app.log", "history.jsonl"); l != "app.log" || h != "history.jsonl" {
		t.Fatalf("default policy must keep paths: %q %q", l, h)
	}
	if l, h := EffectiveStoragePaths(true, "app.log", "history.jsonl"); l != "" || h != "" {
		t.Fatalf("NO_CONTENT_LOGS must clear paths: %q %q", l, h)
	}
}

func TestWarnStaleContentFiles(t *testing.T) {
	dir := t.TempDir()
	hist := filepath.Join(dir, "history.jsonl")
	if err := os.WriteFile(hist, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := WarnStaleContentFiles(filepath.Join(dir, "app.log"), hist); len(got) != 1 {
		t.Fatalf("one stale file must produce one warning, got %v", got)
	}
	if got := WarnStaleContentFiles(filepath.Join(dir, "absent.log"), filepath.Join(dir, "absent.jsonl")); len(got) != 0 {
		t.Fatalf("no files means no warnings, got %v", got)
	}
	// Blank paths are skipped, never reported as stale.
	if got := WarnStaleContentFiles("", ""); len(got) != 0 {
		t.Fatalf("blank paths must be skipped, got %v", got)
	}
}

func TestResolveUnderRootAndDataPath(t *testing.T) {
	if got := ResolveUnderRoot("instances/prod", "./data/app.log"); got != filepath.Join("instances/prod", "data/app.log") {
		t.Fatalf("relative must join root, got %q", got)
	}
	if got := ResolveUnderRoot("instances/prod", "/srv/app.log"); got != "/srv/app.log" {
		t.Fatalf("absolute must stay, got %q", got)
	}
	if got := ResolveUnderRoot("instances/prod", "  "); got != "" {
		t.Fatalf("blank must stay blank, got %q", got)
	}
	t.Setenv("DATA_DIR", "")
	if got := DataPath("/srv/inst", "app.log"); got != filepath.Join("/srv/inst", "data", "app.log") {
		t.Fatalf("default data path = %q", got)
	}
	t.Setenv("DATA_DIR", "/srv/v2v")
	if got := DataPath("/srv/inst", "history.jsonl"); got != "/srv/v2v/history.jsonl" {
		t.Fatalf("DATA_DIR override = %q", got)
	}
}

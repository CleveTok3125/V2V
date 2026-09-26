package serverconfig

import (
	"strings"
	"testing"
)

// setAbuseEnv pins every abuse variable: group A always, group B for the
// enabled case. Tests unset or corrupt single keys afterwards.
func setAbuseEnv(t *testing.T, enabled bool) {
	t.Helper()
	t.Setenv("MAX_TOTAL_CONNECTIONS", "500")
	t.Setenv("ATTACK_ENTER_SECS", "5s")
	t.Setenv("ATTACK_ENTER_RPS", "50")
	t.Setenv("UNDER_ATTACK_TTL", "10m")
	t.Setenv("UNDER_ATTACK_FORCE", "auto")
	t.Setenv("POW_FIRST_CONNECT", "under-attack")
	t.Setenv("POW_GATE_TIER", "1")
	t.Setenv("POW_TIER_COUNT", "4")
	for _, n := range []string{"1", "2", "3"} {
		t.Setenv("POW_P"+n+"_T", "1")
		t.Setenv("POW_P"+n+"_M", "8192")
		t.Setenv("POW_P"+n+"_P", "1")
		t.Setenv("POW_P"+n+"_DIFF", "16")
		t.Setenv("POW_P"+n+"_EST_MS", "500")
	}
	t.Setenv("POW_TTL", "2m")
	t.Setenv("JOIN_WAIT_MIN", "30s")
	t.Setenv("JOIN_WAIT_MAX", "60s")
	t.Setenv("SCREEN_DEADLINE", "5m")
	t.Setenv("GATE_HTTP_ENABLED", "true")
	t.Setenv("GATE_HTTP_CLASSES", "trip_verify,webauthn_finish")
	t.Setenv("GATE_HTTP_MODE", "under-attack")
	t.Setenv("GATE_HTTP_TIER_MIN", "1")
	t.Setenv("PASS_TTL", "10m")
	t.Setenv("PASS_SINGLE_USE", "false")
	if enabled {
		t.Setenv("BEHAVIOR_ENABLED", "true")
	} else {
		t.Setenv("BEHAVIOR_ENABLED", "false")
	}
	t.Setenv("BEHAVIOR_CURVE_GAMMA", "2")
	t.Setenv("BEHAVIOR_WINDOW_SHORT", "10m")
	t.Setenv("BEHAVIOR_WINDOW_LONG", "1h")
	t.Setenv("BEHAVIOR_GAP_WINDOW", "30m")
	t.Setenv("BEHAVIOR_NIGHT_START", "2")
	t.Setenv("BEHAVIOR_NIGHT_END", "5")
	t.Setenv("BEHAVIOR_IDENTITY_GUEST", "1")
	t.Setenv("BEHAVIOR_IDENTITY_TRIP", "0.4")
	t.Setenv("BEHAVIOR_IDENTITY_KEY", "0.15")
	for _, f := range []string{"RHYTHM", "THROUGHPUT", "NIGHT", "CONTINUITY", "CHURN", "IDENTITY", "IPREP", "PROTOERR", "HTTP_RATE", "HTTP_ERROR", "ENDPOINT_FOCUS", "AUTH_PROBE", "ENVELOPE"} {
		t.Setenv("BEHAVIOR_W_"+f, "0.1")
		t.Setenv("BEHAVIOR_NMIN_"+f, "5")
		t.Setenv("BEHAVIOR_RAMP_"+f+"_LO", "0")
		t.Setenv("BEHAVIOR_RAMP_"+f+"_HI", "1")
	}
	t.Setenv("BEHAVIOR_GROUP_BETA_IP", "0.6")
	t.Setenv("BEHAVIOR_GROUP_BETA_48", "0.5")
	t.Setenv("BEHAVIOR_GROUP_BETA_ASN", "0.3")
	t.Setenv("BEHAVIOR_GROUP_BETA_COUNTRY", "0.2")
	t.Setenv("BEHAVIOR_GROUP_MIN_MEMBERS", "2")
	t.Setenv("BEHAVIOR_TIER1_ENTER", "0.25")
	t.Setenv("BEHAVIOR_TIER1_EXIT", "0.18")
	t.Setenv("BEHAVIOR_TIER2_ENTER", "0.55")
	t.Setenv("BEHAVIOR_TIER2_EXIT", "0.45")
	t.Setenv("BEHAVIOR_TIER3_ENTER", "0.8")
	t.Setenv("BEHAVIOR_TIER3_EXIT", "0.7")
	t.Setenv("POW_RECHECK_MIN", "5m")
	t.Setenv("POW_RECHECK_MAX", "15m")
	t.Setenv("BEHAVIOR_SCORE_EVERY_N_MSGS", "20")
	t.Setenv("ATTACK_SCALE_MODE", "max")
	t.Setenv("ATTACK_SCALE_W_REJECT", "1")
	t.Setenv("ATTACK_SCALE_W_CONNRATE", "1")
	t.Setenv("ATTACK_SCALE_W_IPGROWTH", "1")
	t.Setenv("ATTACK_SCALE_W_BLOCKLIST", "1")
	t.Setenv("ATTACK_TIER_BUMP_MAX", "1")
	t.Setenv("BEHAVIOR_RETENTION_P0", "24h")
	t.Setenv("BEHAVIOR_RETENTION_P1", "72h")
	t.Setenv("BEHAVIOR_RETENTION_P2", "168h")
	t.Setenv("BEHAVIOR_RETENTION_P3", "720h")
	t.Setenv("BEHAVIOR_STATS_ENABLED", "true")
	t.Setenv("BEHAVIOR_STATS_WINDOW", "15m")
}

func TestLoadAbuseHappyPath(t *testing.T) {
	setAbuseEnv(t, true)
	cfg, warns, err := LoadAbuseConfig()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(warns) != 0 {
		t.Fatalf("clean config must not warn: %v", warns)
	}
	if cfg.MaxTotalConnections != 500 || cfg.PowTierCount != 4 {
		t.Fatalf("core fields wrong: %+v", cfg)
	}
	if cfg.Behavior == nil {
		t.Fatal("behavior must load when enabled")
	}
	if len(cfg.PowTiers) != 4 || len(cfg.Behavior.TierEnter) != 4 || len(cfg.Behavior.TierExit) != 4 {
		t.Fatalf("tier slices wrong: %+v", cfg)
	}
	if len(cfg.Behavior.Features) != 13 {
		t.Fatalf("want 13 features, got %d", len(cfg.Behavior.Features))
	}
}

func TestLoadAbuseDisabledSkipsGroupB(t *testing.T) {
	setAbuseEnv(t, false)
	// Group B keys absent must be fine when disabled.
	for _, k := range []string{"BEHAVIOR_CURVE_GAMMA", "BEHAVIOR_TIER1_ENTER", "POW_RECHECK_MIN"} {
		t.Setenv(k, "")
	}
	cfg, _, err := LoadAbuseConfig()
	if err != nil {
		t.Fatalf("disabled load: %v", err)
	}
	if cfg.Behavior != nil {
		t.Fatal("behavior must be nil when disabled")
	}
}

func TestLoadAbuseMissingKeyFails(t *testing.T) {
	setAbuseEnv(t, true)
	t.Setenv("MAX_TOTAL_CONNECTIONS", "")
	t.Setenv("BEHAVIOR_TIER2_ENTER", "")
	_, _, err := LoadAbuseConfig()
	if err == nil {
		t.Fatal("missing keys must fail")
	}
	if !strings.Contains(err.Error(), "MAX_TOTAL_CONNECTIONS") {
		t.Fatalf("group-A error must name the key: %v", err)
	}
	// Restore group A: the group-B miss must surface next.
	t.Setenv("MAX_TOTAL_CONNECTIONS", "500")
	_, _, err = LoadAbuseConfig()
	if err == nil {
		t.Fatal("missing keys must fail")
	}
	if !strings.Contains(err.Error(), "BEHAVIOR_TIER2_ENTER") {
		t.Fatalf("group-B error must name the key: %v", err)
	}
}

func TestLoadAbuseBadHysteresisFails(t *testing.T) {
	setAbuseEnv(t, true)
	t.Setenv("BEHAVIOR_TIER2_EXIT", "0.6") // above enter 0.55
	if _, _, err := LoadAbuseConfig(); err == nil {
		t.Fatal("overlapping hysteresis must fail")
	}
}

func TestLoadAbuseBadEnumFails(t *testing.T) {
	setAbuseEnv(t, true)
	t.Setenv("UNDER_ATTACK_FORCE", "sometimes")
	if _, _, err := LoadAbuseConfig(); err == nil {
		t.Fatal("bad enum must fail")
	}
}

func TestLoadAbuseBadTierCountFails(t *testing.T) {
	setAbuseEnv(t, true)
	t.Setenv("POW_TIER_COUNT", "1")
	if _, _, err := LoadAbuseConfig(); err == nil {
		t.Fatal("tier count 1 must fail")
	}
}

func TestLoadAbuseBadRampFails(t *testing.T) {
	setAbuseEnv(t, true)
	t.Setenv("BEHAVIOR_RAMP_CHURN_LO", "9")
	t.Setenv("BEHAVIOR_RAMP_CHURN_HI", "9")
	if _, _, err := LoadAbuseConfig(); err == nil {
		t.Fatal("degenerate ramp must fail")
	}
}

func TestLoadAbuseBadGroupARanges(t *testing.T) {
	for _, tc := range []struct{ key, val string }{
		{"MAX_TOTAL_CONNECTIONS", "0"},
		{"ATTACK_ENTER_SECS", "0s"},
		{"ATTACK_ENTER_RPS", "0"},
	} {
		setAbuseEnv(t, true)
		t.Setenv(tc.key, tc.val)
		if _, _, err := LoadAbuseConfig(); err == nil {
			t.Fatalf("%s=%s must fail", tc.key, tc.val)
		}
	}
}

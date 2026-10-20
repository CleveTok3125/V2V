package serverconfig

import "testing"

// SOFT_CAP_RATIO is optional: an unset value loads the default without
// being reported as a missing required key. A deployment that predates
// the knob must boot with the old 80% behavior.
func TestSoftCapRatioDefaultsWhenUnset(t *testing.T) {
	setAbuseEnv(t, false)

	cfg, _, err := LoadAbuseConfig()
	if err != nil {
		t.Fatalf("load without SOFT_CAP_RATIO: %v", err)
	}
	if cfg.SoftCapRatio != DefaultSoftCapRatio {
		t.Fatalf("SoftCapRatio = %v, want the default %v", cfg.SoftCapRatio, DefaultSoftCapRatio)
	}
}

// An operator who does set it gets their value.
func TestSoftCapRatioTakesTheConfiguredValue(t *testing.T) {
	setAbuseEnv(t, false)
	t.Setenv("SOFT_CAP_RATIO", "0.5")

	cfg, _, err := LoadAbuseConfig()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.SoftCapRatio != 0.5 {
		t.Fatalf("SoftCapRatio = %v, want 0.5", cfg.SoftCapRatio)
	}
}

// Outside (0,1] the share is meaningless: 0 would degrade from the first
// connection, above 1 never. Both are operator typos, so they fail the
// boot rather than silently shifting the threshold.
func TestSoftCapRatioRejectsOutOfRange(t *testing.T) {
	for _, bad := range []string{"0", "-0.1", "1.5"} {
		t.Run(bad, func(t *testing.T) {
			setAbuseEnv(t, false)
			t.Setenv("SOFT_CAP_RATIO", bad)
			if _, _, err := LoadAbuseConfig(); err == nil {
				t.Fatalf("SOFT_CAP_RATIO=%s was accepted", bad)
			}
		})
	}
}

// A non-numeric value is a typo too. It must be reported, not swallowed
// into the default.
func TestSoftCapRatioRejectsGarbage(t *testing.T) {
	setAbuseEnv(t, false)
	t.Setenv("SOFT_CAP_RATIO", "half")
	if _, _, err := LoadAbuseConfig(); err == nil {
		t.Fatal("a non-numeric ratio was accepted")
	}
}

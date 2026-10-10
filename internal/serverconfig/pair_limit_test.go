package serverconfig

import "testing"

// PAIR_MAX_PENDING is optional: it is the one pairing knob an operator may
// never touch, so an unset value has to load with the default and without
// being reported as a missing required key. A template that omits it, or a
// deployment that predates it, must both boot.
func TestPairMaxPendingDefaultsWhenUnset(t *testing.T) {
	setAbuseEnv(t, false)

	cfg, _, err := LoadAbuseConfig()
	if err != nil {
		t.Fatalf("load without PAIR_MAX_PENDING: %v", err)
	}
	if cfg.PairMaxPending != DefaultPairMaxPending {
		t.Fatalf("PairMaxPending = %d, want the default %d", cfg.PairMaxPending, DefaultPairMaxPending)
	}
}

// An operator who does set it gets their value. The ceiling is the only
// bound on pairing memory, so silently ignoring it would be a leak.
func TestPairMaxPendingTakesTheConfiguredValue(t *testing.T) {
	setAbuseEnv(t, false)
	t.Setenv("PAIR_MAX_PENDING", "7")

	cfg, _, err := LoadAbuseConfig()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.PairMaxPending != 7 {
		t.Fatalf("PairMaxPending = %d, want 7", cfg.PairMaxPending)
	}
}

// A zero ceiling would refuse every pairing, and a negative one would make
// the comparison meaningless. Both are operator typos, so they fail the boot
// rather than disabling the feature quietly.
func TestPairMaxPendingRejectsNonPositive(t *testing.T) {
	for _, bad := range []string{"0", "-1"} {
		t.Run(bad, func(t *testing.T) {
			setAbuseEnv(t, false)
			t.Setenv("PAIR_MAX_PENDING", bad)
			if _, _, err := LoadAbuseConfig(); err == nil {
				t.Fatalf("PAIR_MAX_PENDING=%s was accepted", bad)
			}
		})
	}
}

// A non-numeric value is a typo too. It must be reported, not swallowed into
// the default: an operator who wrote "1o24" should not believe it took.
func TestPairMaxPendingRejectsGarbage(t *testing.T) {
	setAbuseEnv(t, false)
	t.Setenv("PAIR_MAX_PENDING", "1o24")
	if _, _, err := LoadAbuseConfig(); err == nil {
		t.Fatal("a non-numeric ceiling was accepted")
	}
}

package serverconfig

import "testing"

func TestChallengeFeatureLoads(t *testing.T) {
	setAbuseEnv(t, true)
	t.Setenv("BEHAVIOR_W_CHALLENGE", "0.12")
	t.Setenv("BEHAVIOR_NMIN_CHALLENGE", "1")
	t.Setenv("BEHAVIOR_RAMP_CHALLENGE_LO", "1")
	t.Setenv("BEHAVIOR_RAMP_CHALLENGE_HI", "8")
	cfg, _, err := LoadAbuseConfig()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	fp, ok := cfg.Behavior.Features["challenge"]
	if !ok {
		t.Fatal("CHALLENGE feature must load")
	}
	if fp.Weight != 0.12 || fp.NMin != 1 || fp.Lo != 1 || fp.Hi != 8 {
		t.Fatalf("challenge params wrong: %+v", fp)
	}
}

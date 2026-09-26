package main

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/CleveTok3125/V2V/internal/behavior"
	"github.com/CleveTok3125/V2V/internal/serverconfig"
	"github.com/joho/godotenv"
)

// calibTemplatePath is the shipped instance template the candidate
// values must track. Tests run with the package dir as cwd.
const calibTemplatePath = "../template/server/instances/default/.env"

// TestCandidateBehaviorMatchesTemplate pins candidateBehaviorConfig to
// the shipped template so the calibration harness cannot silently drift
// from the configuration operators actually get. Durations are parsed,
// every other knob is compared as a float.
func TestCandidateBehaviorMatchesTemplate(t *testing.T) {
	vals, err := godotenv.Read(calibTemplatePath)
	if err != nil {
		t.Fatalf("read template: %v", err)
	}
	bc := candidateBehaviorConfig()
	checkFloat := func(key string, got float64) {
		t.Helper()
		raw, ok := vals[key]
		if !ok {
			t.Errorf("%s missing from template", key)
			return
		}
		want, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			t.Errorf("%s: template %q not numeric", key, raw)
			return
		}
		if want != got {
			t.Errorf("%s: candidate %v != template %v", key, got, want)
		}
	}
	checkFloat("BEHAVIOR_CURVE_GAMMA", bc.CurveGamma)
	checkFloat("BEHAVIOR_NIGHT_START", float64(bc.NightStart))
	checkFloat("BEHAVIOR_NIGHT_END", float64(bc.NightEnd))
	checkFloat("BEHAVIOR_IDENTITY_GUEST", bc.IdentityVals["guest"])
	checkFloat("BEHAVIOR_IDENTITY_TRIP", bc.IdentityVals["trip"])
	checkFloat("BEHAVIOR_IDENTITY_KEY", bc.IdentityVals["key"])
	checkFloat("BEHAVIOR_GROUP_BETA_IP", bc.GroupBetaIP)
	checkFloat("BEHAVIOR_GROUP_BETA_48", bc.GroupBeta48)
	checkFloat("BEHAVIOR_GROUP_BETA_ASN", bc.GroupBetaASN)
	checkFloat("BEHAVIOR_GROUP_BETA_COUNTRY", bc.GroupBetaCT)
	checkFloat("BEHAVIOR_GROUP_MIN_MEMBERS", float64(bc.GroupMinMemb))
	for n := 1; n < len(bc.TierEnter); n++ {
		checkFloat("BEHAVIOR_TIER"+strconv.Itoa(n)+"_ENTER", bc.TierEnter[n])
		checkFloat("BEHAVIOR_TIER"+strconv.Itoa(n)+"_EXIT", bc.TierExit[n])
	}
	for _, name := range []string{
		"RHYTHM", "THROUGHPUT", "NIGHT", "CONTINUITY", "CHURN", "IDENTITY",
		"IPREP", "PROTOERR", "HTTP_RATE", "HTTP_ERROR", "ENDPOINT_FOCUS",
		"AUTH_PROBE", "ENVELOPE",
	} {
		fp, ok := bc.Features[behavior.FeatureID(strings.ToLower(name))]
		if !ok {
			t.Errorf("feature %s missing from candidate", name)
			continue
		}
		checkFloat("BEHAVIOR_W_"+name, fp.Weight)
		checkFloat("BEHAVIOR_NMIN_"+name, float64(fp.NMin))
		checkFloat("BEHAVIOR_RAMP_"+name+"_LO", fp.Lo)
		checkFloat("BEHAVIOR_RAMP_"+name+"_HI", fp.Hi)
	}
	checkDur := func(key string, got time.Duration) {
		t.Helper()
		want, err := time.ParseDuration(vals[key])
		if err != nil {
			t.Errorf("%s: template %q not a duration", key, vals[key])
			return
		}
		if want != got {
			t.Errorf("%s: candidate %v != template %v", key, got, want)
		}
	}
	checkDur("BEHAVIOR_WINDOW_SHORT", bc.WindowShort)
	checkDur("BEHAVIOR_WINDOW_LONG", bc.WindowLong)
	checkDur("BEHAVIOR_GAP_WINDOW", bc.GapWindow)
}

// Calibration harness: replays event sequences observed in production
// through the real scorer and pins the tier policy —
//   - sustained max-rate flood (3-5 msg/s for minutes) -> tier 2
//   - slower flood (~1 msg/s with pauses) -> tier 1
//   - join/leave flap without messages -> tier 1
//   - legit baselines (casual chat, lurkers, busy-but-human) -> tier 0
//
// Knob changes must keep this green; tune the candidate below until
// every vector lands, then copy the winners to template/.env.
func candidateBehaviorConfig() *serverconfig.BehaviorConfig {
	bc := &serverconfig.BehaviorConfig{
		CurveGamma:   1.5,
		WindowShort:  10 * time.Minute,
		WindowLong:   time.Hour,
		GapWindow:    30 * time.Minute,
		NightStart:   2,
		NightEnd:     5,
		IdentityVals: map[string]float64{"guest": 0.5, "trip": 0.2, "key": 0.08},
		Features:     map[behavior.FeatureID]behavior.FeatureParam{},
		GroupBetaIP:  0.6,
		GroupBeta48:  0.5,
		GroupBetaASN: 0.3,
		GroupBetaCT:  0.2,
		GroupMinMemb: 2,
		TierEnter:    []float64{0, 0.12, 0.30, 0.65},
		TierExit:     []float64{0, 0.08, 0.24, 0.55},
	}
	set := func(id behavior.FeatureID, w float64, nmin int, lo, hi float64, highBad bool) {
		bc.Features[id] = behavior.FeatureParam{Weight: w, NMin: nmin, Lo: lo, Hi: hi, HighBad: highBad}
	}
	set(behavior.F_RHYTHM, 0.18, 20, 0.1, 0.8, false)
	set(behavior.F_THROUGHPUT, 0.25, 10, 20, 200, true)
	set(behavior.F_NIGHT, 0.12, 30, 0.05, 0.6, true)
	set(behavior.F_CONTINUITY, 0.12, 10, 4, 16, true)
	set(behavior.F_CHURN, 0.10, 5, 3, 10, true)
	set(behavior.F_IDENTITY, 0.10, 1, 0, 1, true)
	set(behavior.F_IPREP, 0.15, 5, 5, 50, true)
	set(behavior.F_PROTOERR, 0.08, 1, 0, 5, true)
	set(behavior.F_HTTP_RATE, 0.10, 5, 1, 30, true)
	set(behavior.F_HTTP_ERROR, 0.10, 5, 0, 1, true)
	set(behavior.F_ENDPOINT_FOCUS, 0.10, 5, 0, 1, true)
	set(behavior.F_AUTH_PROBE, 0.10, 1, 0, 5, true)
	set(behavior.F_ENVELOPE, 0.10, 1, 0, 5, true)
	return bc
}

func calibDay() time.Time {
	// 15:00 UTC: outside the 02-05 night window.
	return time.Date(2026, 9, 26, 15, 0, 0, 0, time.UTC)
}

func feedMsgs(eng *BehaviorEngine, ip string, start time.Time, gaps []time.Duration) time.Time {
	t := start
	for _, g := range gaps {
		t = t.Add(g)
		eng.ObserveMessage(ip, t)
	}
	return t
}

func checkTier(t *testing.T, name, ip string, gaps []time.Duration, conns []time.Duration, want int) {
	t.Helper()
	eng := NewBehaviorEngine(behavior.NewStore(100), behavior.NewStats(), stubGeo{}, "")
	base := calibDay()
	if len(conns) == 0 {
		eng.ObserveConnect(ip, "u", behavior.IdGuest, base)
	} else {
		for _, d := range conns {
			eng.ObserveConnect(ip, "u", behavior.IdGuest, base.Add(d))
		}
	}
	end := feedMsgs(eng, ip, base, gaps)
	tier, s := eng.Score(ip, candidateBehaviorConfig(), end, time.UTC)
	t.Logf("%s: score=%.3f tier=%d (want %d)", name, s, tier, want)
	if tier != want {
		t.Errorf("%s: got tier %d score %.3f, want tier %d", name, tier, s, want)
	}
}

func floodGaps(n int, fast, slow time.Duration, slowEvery int) []time.Duration {
	gaps := make([]time.Duration, 0, n)
	for i := 0; i < n; i++ {
		if slowEvery > 0 && i%slowEvery == slowEvery-1 {
			gaps = append(gaps, slow)
			continue
		}
		gaps = append(gaps, fast)
	}
	return gaps
}

func TestBehaviorCalibration(t *testing.T) {
	// Sustained max-rate flood: 231 msgs in ~80s at ~0.27s cadence
	// with occasional half-second gaps.
	t.Run("maxRateFlood", func(t *testing.T) {
		gaps := floodGaps(231, 270*time.Millisecond, 550*time.Millisecond, 10)
		checkTier(t, "maxRateFlood", "10.1.0.1", gaps, nil, 2)
	})
	// Slower flood: ~160 msgs in ~3min with multi-second pauses.
	t.Run("slowFlood", func(t *testing.T) {
		gaps := floodGaps(160, 400*time.Millisecond, 8*time.Second, 20)
		checkTier(t, "slowFlood", "10.1.0.2", gaps, nil, 1)
	})
	// Join/leave flapping: 5 reconnects within a minute, no messages.
	t.Run("rejoinFlap", func(t *testing.T) {
		conns := []time.Duration{0, 8 * time.Second, 15 * time.Second, 30 * time.Second, 45 * time.Second}
		checkTier(t, "rejoinFlap", "10.1.0.3", nil, conns, 1)
	})
	// Casual baseline: 7 msgs over ~2min.
	t.Run("casualChat", func(t *testing.T) {
		gaps := []time.Duration{4 * time.Second, 19 * time.Second, 46 * time.Second, 10 * time.Second, 38 * time.Second, 4 * time.Second}
		checkTier(t, "casualChat", "10.1.0.4", gaps, nil, 0)
	})
	// Lurker: join/leave only.
	t.Run("lurker", func(t *testing.T) {
		conns := []time.Duration{0, 90 * time.Second}
		checkTier(t, "lurker", "10.1.0.5", nil, conns, 0)
	})
	// Synthetic busy-but-human: 60 msgs/hour at irregular cadence.
	t.Run("busyLegit", func(t *testing.T) {
		gaps := make([]time.Duration, 0, 60)
		for i := 0; i < 60; i++ {
			if i%2 == 0 {
				gaps = append(gaps, 20*time.Second)
			} else {
				gaps = append(gaps, 100*time.Second)
			}
		}
		checkTier(t, "busyLegit", "10.1.0.6", gaps, nil, 0)
	})
}

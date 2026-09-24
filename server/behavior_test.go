package main

import (
	"testing"
	"time"

	"github.com/CleveTok3125/V2V/internal/behavior"
	"github.com/CleveTok3125/V2V/internal/serverconfig"
)

type stubGeo struct{}

func (stubGeo) Lookup(ip string) (behavior.GeoInfo, bool) { return behavior.GeoInfo{}, false }

func testBehaviorConfig() *serverconfig.BehaviorConfig {
	bc := &serverconfig.BehaviorConfig{
		CurveGamma:   2,
		WindowShort:  10 * time.Minute,
		WindowLong:   time.Hour,
		GapWindow:    30 * time.Minute,
		NightStart:   2,
		NightEnd:     5,
		IdentityVals: map[string]float64{"guest": 1, "trip": 0.4, "key": 0.15},
		Features:     map[behavior.FeatureID]behavior.FeatureParam{},
		GroupBetaIP:  0.6,
		GroupBeta48:  0.5,
		GroupBetaASN: 0.3,
		GroupBetaCT:  0.2,
		GroupMinMemb: 2,
		TierEnter:    []float64{0, 0.25, 0.55, 0.8},
		TierExit:     []float64{0, 0.18, 0.45, 0.7},
	}
	for _, f := range []behavior.FeatureID{
		behavior.F_RHYTHM, behavior.F_THROUGHPUT, behavior.F_NIGHT,
		behavior.F_CONTINUITY, behavior.F_CHURN, behavior.F_IDENTITY,
		behavior.F_IPREP, behavior.F_PROTOERR, behavior.F_HTTP_RATE,
		behavior.F_HTTP_ERROR, behavior.F_ENDPOINT_FOCUS,
		behavior.F_AUTH_PROBE, behavior.F_ENVELOPE,
	} {
		bc.Features[f] = behavior.FeatureParam{Weight: 0.1, NMin: 5, Lo: 0, Hi: 1, HighBad: true}
	}
	// Rhythm ramps down like production.
	r := bc.Features[behavior.F_RHYTHM]
	r.Lo, r.Hi, r.HighBad, r.NMin = 0.1, 0.8, false, 20
	bc.Features[behavior.F_RHYTHM] = r
	return bc
}

func TestEngineColdStartTierZero(t *testing.T) {
	eng := NewBehaviorEngine(behavior.NewStore(100), behavior.NewStats(), stubGeo{}, "")
	tier, s := eng.Score("10.0.0.9", testBehaviorConfig(), time.Now(), time.UTC)
	if tier != 0 {
		t.Fatalf("fresh IP must score tier 0, got %d (%v)", tier, s)
	}
}

func TestEngineMetronomeEscalates(t *testing.T) {
	eng := NewBehaviorEngine(behavior.NewStore(100), behavior.NewStats(), stubGeo{}, "")
	now := time.Now()
	ip := "10.0.0.10"
	eng.ObserveConnect(ip, "bot", behavior.IdGuest, now.Add(-time.Hour))
	for i := 0; i < 60; i++ {
		eng.ObserveMessage(ip, now.Add(-time.Duration(60-i)*10*time.Second))
	}
	tier, _ := eng.Score(ip, testBehaviorConfig(), now, time.UTC)
	if tier < 1 {
		t.Fatalf("metronome + guest must escalate, got tier %d", tier)
	}
}

func TestEngineGroupTaints(t *testing.T) {
	eng := NewBehaviorEngine(behavior.NewStore(100), behavior.NewStats(), stubGeo{}, "")
	now := time.Now()
	bc := testBehaviorConfig()
	// One bad actor in the /24.
	eng.ObserveConnect("203.0.113.5", "bot", behavior.IdGuest, now.Add(-time.Hour))
	for i := 0; i < 60; i++ {
		eng.ObserveMessage("203.0.113.5", now.Add(-time.Duration(60-i)*10*time.Second))
	}
	badTier, _ := eng.Score("203.0.113.5", bc, now, time.UTC)
	if badTier < 1 {
		t.Fatalf("bad actor must escalate, got %d", badTier)
	}
	// A quiet neighbor in the same /24 picks up group signal but must
	// not be condemned: tier stays at most the lightest PoW.
	tier, s := eng.Score("203.0.113.6", bc, now, time.UTC)
	if tier > 1 {
		t.Fatalf("quiet neighbor must stay near zero, got %d (%v)", tier, s)
	}
	if s <= 0 {
		t.Fatal("group signal must register above zero")
	}
}

func TestEnginePruneCleansIndexes(t *testing.T) {
	eng := NewBehaviorEngine(behavior.NewStore(100), behavior.NewStats(), stubGeo{}, "")
	old := time.Now().Add(-48 * time.Hour)
	eng.ObserveConnect("10.0.0.11", "x", behavior.IdGuest, old)
	eng.Score("10.0.0.11", testBehaviorConfig(), old, time.UTC)
	eng.Prune(time.Now(), map[int]time.Duration{0: time.Hour}, time.Hour)
	if eng.store.Len() != 0 {
		t.Fatal("profile must prune")
	}
	eng.mu.Lock()
	defer eng.mu.Unlock()
	if len(eng.scores) != 0 || len(eng.names) != 0 {
		t.Fatal("indexes must prune with the profile")
	}
}

func TestEngineBlocklistFlag(t *testing.T) {
	eng := NewBehaviorEngine(behavior.NewStore(100), behavior.NewStats(), stubGeo{}, "")
	now := time.Now()
	eng.SetBlocklisted("10.0.0.30")
	tier, score := eng.Score("10.0.0.30", testBehaviorConfig(), now, time.UTC)
	if score <= 0 {
		t.Fatalf("blocklisted IP must score above zero, got %v", score)
	}
	if tier < 1 {
		t.Fatalf("blocklisted IP must be tier >= 1, got %d", tier)
	}
}

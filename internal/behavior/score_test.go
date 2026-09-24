package behavior

import "testing"

func wmap(ws map[FeatureID]float64) map[FeatureID]FeatureParam {
	out := map[FeatureID]FeatureParam{}
	for id, w := range ws {
		out[id] = FeatureParam{Weight: w}
	}
	return out
}

func TestAggregateEmptyFallsBack(t *testing.T) {
	if got := Aggregate(map[FeatureID]float64{}, wmap(nil), 2, 0); got != 0 {
		t.Fatalf("empty + clean IP must score 0, got %v", got)
	}
	if got := Aggregate(map[FeatureID]float64{}, wmap(nil), 2, 1); got != 1 {
		t.Fatalf("empty + blocklisted IP must score 1, got %v", got)
	}
}

func TestAggregateWeightedCurve(t *testing.T) {
	vals := map[FeatureID]float64{F_RHYTHM: 1, F_CHURN: 0}
	params := wmap(map[FeatureID]float64{F_RHYTHM: 0.5, F_CHURN: 0.5, F_NIGHT: 0})
	if got := Aggregate(vals, params, 1, 0); got != 0.5 {
		t.Fatalf("avg must be 0.5, got %v", got)
	}
	if got := Aggregate(vals, params, 2, 0); got != 0.25 {
		t.Fatalf("gamma=2 must square, got %v", got)
	}
}

func TestNextTierHysteresis(t *testing.T) {
	enter := []float64{0, 0.25, 0.55, 0.80}
	exit := []float64{0, 0.18, 0.45, 0.70}
	const maxTier = 3
	cases := []struct {
		s    float64
		cur  int
		want int
	}{
		{0.30, 0, 1}, // crosses enter1
		{0.50, 1, 1}, // below enter2, stays
		{0.60, 1, 2}, // crosses enter2
		{0.50, 2, 2}, // above exit2, stays (no flap)
		{0.40, 2, 1}, // below exit2, drops
		{0.20, 1, 1}, // above exit1, stays
		{0.10, 1, 0}, // below exit1, drops
		{0.90, 0, 3}, // jumps straight to top
		{0.10, 3, 0}, // collapses straight down
	}
	for i, c := range cases {
		if got := NextTier(c.s, c.cur, enter, exit, maxTier); got != c.want {
			t.Fatalf("case %d: s=%v cur=%d got %d want %d", i, c.s, c.cur, got, c.want)
		}
	}
}

func TestGroupAggregate(t *testing.T) {
	if got := GroupAggregate(nil, 0.5); got != 0 {
		t.Fatalf("empty group must score 0, got %v", got)
	}
	// max=1, mean=0.5 -> G = 0.5*(0.6+0.2) = 0.4
	if got := GroupAggregate([]float64{1, 0}, 0.5); got != 0.4 {
		t.Fatalf("got %v want 0.4", got)
	}
	if got := Combine(0.3, 0.4); got != 0.4 {
		t.Fatalf("combine must take max, got %v", got)
	}
	if got := Combine(0.5, 0.4); got != 0.5 {
		t.Fatalf("combine must take max, got %v", got)
	}
}

func TestAttackScale(t *testing.T) {
	if got := AttackScale(0.2, 0.9, 0.1, 0, "max", [4]float64{1, 1, 1, 1}); got != 0.9 {
		t.Fatalf("max mode got %v", got)
	}
	if got := AttackScale(1, 1, 1, 1, "weighted", [4]float64{1, 1, 1, 1}); got != 1 {
		t.Fatalf("weighted full got %v", got)
	}
	if got := AttackScale(5, -1, 0, 0, "max", [4]float64{1, 1, 1, 1}); got != 1 {
		t.Fatalf("inputs must clamp, got %v", got)
	}
	if got := AttackScale(0.2, 0.9, 0.1, 0, "bogus", [4]float64{1, 1, 1, 1}); got != 0.9 {
		t.Fatalf("unknown mode must fall back to max, got %v", got)
	}
}

func TestApplyBump(t *testing.T) {
	if got := ApplyBump(0, 0.9, 1, 3); got != 1 {
		t.Fatalf("got %d want 1", got)
	}
	if got := ApplyBump(2, 1, 1, 3); got != 3 {
		t.Fatalf("bump must clamp at tier max, got %d", got)
	}
	if got := ApplyBump(1, 0.1, 1, 3); got != 1 {
		t.Fatalf("low scale must not bump, got %d", got)
	}
}

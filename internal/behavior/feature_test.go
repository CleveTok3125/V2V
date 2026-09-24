package behavior

import (
	"testing"
	"time"
)

func testCtx(now time.Time) FeatureCtx {
	return FeatureCtx{
		Now:           now,
		Loc:           time.UTC,
		ShortWindow:   10 * time.Minute,
		LongWindow:    time.Hour,
		GapWindow:     30 * time.Minute,
		NightStart:    2,
		NightEnd:      5,
		IdentityGuest: 1.0,
		IdentityTrip:  0.4,
		IdentityKey:   0.15,
	}
}

func fp(w float64, nmin int, lo, hi float64, highBad bool) FeatureParam {
	return FeatureParam{Weight: w, NMin: nmin, Lo: lo, Hi: hi, HighBad: highBad}
}

func TestRamp(t *testing.T) {
	if got := Ramp(5, 0, 10, true); got != 0.5 {
		t.Fatalf("rampHigh mid = %v", got)
	}
	if got := Ramp(-5, 0, 10, true); got != 0 {
		t.Fatalf("rampHigh below lo must clamp 0, got %v", got)
	}
	if got := Ramp(50, 0, 10, true); got != 1 {
		t.Fatalf("rampHigh above hi must clamp 1, got %v", got)
	}
	if got := Ramp(2, 0, 10, false); got != 0.8 {
		t.Fatalf("rampLow = %v", got)
	}
	if got := Ramp(0, 5, 5, true); got != 0 {
		t.Fatalf("degenerate lo==hi with x below must be 0, got %v", got)
	}
}

func TestRhythmRegularIsSuspicious(t *testing.T) {
	now := time.Now()
	p := &Profile{}
	for i := 0; i < 30; i++ {
		p.AddMessage(now.Add(-time.Duration(30-i) * 10 * time.Second))
	}
	v, ok := FeatureValue(p, F_RHYTHM, fp(0.18, 20, 0.1, 0.8, false), testCtx(now))
	if !ok {
		t.Fatal("regular stream must be valid")
	}
	if v < 0.9 {
		t.Fatalf("metronome stream must score high, got %v", v)
	}
}

func TestRhythmBurstyIsHuman(t *testing.T) {
	now := time.Now()
	p := &Profile{}
	t0 := now.Add(-time.Hour)
	p.AddMessage(t0)
	p.AddMessage(t0.Add(time.Second))
	p.AddMessage(t0.Add(50 * time.Minute))
	p.AddMessage(t0.Add(50*time.Minute + time.Second))
	p.AddMessage(t0.Add(59 * time.Minute))
	for i := 0; i < 25; i++ {
		p.AddMessage(t0.Add(time.Duration(30+i) * time.Minute))
	}
	v, ok := FeatureValue(p, F_RHYTHM, fp(0.18, 20, 0.1, 0.8, false), testCtx(now))
	if !ok {
		t.Fatal("must be valid")
	}
	if v > 0.5 {
		t.Fatalf("bursty stream must score low, got %v", v)
	}
}

func TestThroughput(t *testing.T) {
	now := time.Now()
	p := &Profile{}
	for i := 0; i < 200; i++ {
		p.AddMessage(now.Add(-time.Duration(i) * 18 * time.Second))
	}
	v, ok := FeatureValue(p, F_THROUGHPUT, fp(0.15, 10, 20, 300, true), testCtx(now))
	if !ok || v < 0.5 {
		t.Fatalf("200/h must be suspicious, got %v ok=%v", v, ok)
	}
}

func TestNightFraction(t *testing.T) {
	day := time.Date(2026, 9, 24, 3, 0, 0, 0, time.UTC)
	p := &Profile{}
	for i := 0; i < 30; i++ {
		p.AddMessage(day.Add(time.Duration(i) * time.Minute))
	}
	v, ok := FeatureValue(p, F_NIGHT, fp(0.12, 10, 0.05, 0.6, true), testCtx(day.Add(30*time.Minute)))
	if !ok || v < 0.9 {
		t.Fatalf("all-night stream must score high, got %v ok=%v", v, ok)
	}
}

func TestContinuity(t *testing.T) {
	now := time.Now()
	p := &Profile{}
	for i := 0; i < 120; i++ {
		p.AddMessage(now.Add(-time.Duration(120-i) * 5 * time.Minute))
	}
	v, ok := FeatureValue(p, F_CONTINUITY, fp(0.12, 10, 4, 16, true), testCtx(now))
	if !ok || v < 0.3 {
		t.Fatalf("10h run must score, got %v ok=%v", v, ok)
	}
}

func TestChurn(t *testing.T) {
	now := time.Now()
	p := &Profile{}
	for i := 0; i < 30; i++ {
		p.AddConnect(now.Add(-time.Duration(i) * 2 * time.Minute))
	}
	v, ok := FeatureValue(p, F_CHURN, fp(0.10, 5, 3, 30, true), testCtx(now))
	if !ok || v < 0.9 {
		t.Fatalf("30 conn/h must score high, got %v ok=%v", v, ok)
	}
}

func TestIdentityAndIPRep(t *testing.T) {
	p := &Profile{}
	v, ok := FeatureValue(p, F_IDENTITY, fp(0.10, 1, 0, 0, true), testCtx(time.Now()))
	if !ok || v != 1.0 {
		t.Fatalf("default guest must score 1.0, got %v ok=%v", v, ok)
	}
	p.SetIdentity(IdKey)
	v, _ = FeatureValue(p, F_IDENTITY, fp(0.10, 1, 0, 0, true), testCtx(time.Now()))
	if v != 0.15 {
		t.Fatalf("key must score 0.15, got %v", v)
	}
	if _, ok := FeatureValue(p, F_IPREP, fp(0.15, 5, 5, 50, true), testCtx(time.Now())); ok {
		t.Fatal("clean IP with no identities must be invalid")
	}
	p.IdentityCount = 40
	v, ok = FeatureValue(p, F_IPREP, fp(0.15, 5, 5, 50, true), testCtx(time.Now()))
	if !ok || v <= 0 || v >= 1 {
		t.Fatalf("40 identities must score mid-range, got %v ok=%v", v, ok)
	}
	p.SetBlocklisted()
	v, ok = FeatureValue(p, F_IPREP, fp(0.15, 5, 5, 50, true), testCtx(time.Now()))
	if !ok || v != 1 {
		t.Fatalf("blocklisted must score 1, got %v ok=%v", v, ok)
	}
}

func TestErrFamilies(t *testing.T) {
	now := time.Now()
	p := &Profile{}
	p.AddErr(ErrEvent{Time: now, Code: "proto:bad-seq"})
	p.AddErr(ErrEvent{Time: now, Code: "auth:invalid-role"})
	p.AddErr(ErrEvent{Time: now, Code: "env:tmpid-zero"})
	param := fp(0.08, 1, 0, 5, true)
	if v, ok := FeatureValue(p, F_PROTOERR, param, testCtx(now)); !ok || v != 0.2 {
		t.Fatalf("one proto err must score 0.2, got %v ok=%v", v, ok)
	}
	if v, ok := FeatureValue(p, F_AUTH_PROBE, param, testCtx(now)); !ok || v != 0.2 {
		t.Fatalf("one auth err must score 0.2, got %v ok=%v", v, ok)
	}
	if v, ok := FeatureValue(p, F_ENVELOPE, param, testCtx(now)); !ok || v != 0.2 {
		t.Fatalf("one env err must score 0.2, got %v ok=%v", v, ok)
	}
}

func TestHTTPFeatures(t *testing.T) {
	now := time.Now()
	p := &Profile{}
	for i := 0; i < 20; i++ {
		st := 200
		if i%2 == 0 {
			st = 429
		}
		p.AddHTTP(HTTPEvent{Time: now.Add(-time.Duration(i) * 20 * time.Second), Class: "trip_verify", Status: st})
	}
	ctx := testCtx(now)
	if v, ok := FeatureValue(p, F_HTTP_RATE, fp(0.1, 5, 1, 30, true), ctx); !ok || v <= 0 {
		t.Fatalf("http rate must score, got %v ok=%v", v, ok)
	}
	if v, ok := FeatureValue(p, F_HTTP_ERROR, fp(0.1, 5, 0, 1, true), ctx); !ok || v != 0.5 {
		t.Fatalf("half errors must score 0.5, got %v ok=%v", v, ok)
	}
	if v, ok := FeatureValue(p, F_ENDPOINT_FOCUS, fp(0.1, 5, 0, 1, true), ctx); !ok || v != 1 {
		t.Fatalf("single class must score 1, got %v ok=%v", v, ok)
	}
}

func TestEmptyProfileInvalid(t *testing.T) {
	p := &Profile{}
	ctx := testCtx(time.Now())
	for _, id := range []FeatureID{F_RHYTHM, F_THROUGHPUT, F_NIGHT, F_CONTINUITY, F_CHURN, F_PROTOERR} {
		if _, ok := FeatureValue(p, id, fp(0.1, 5, 0, 1, true), ctx); ok {
			t.Fatalf("%s must be invalid on empty profile", id)
		}
	}
}

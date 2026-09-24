package behavior

import (
	"math"
	"strings"
	"time"
)

// FeatureID names one anomaly signal. The set is fixed in code (adding a
// new signal kind needs code); every threshold, floor and weight rides
// in FeatureParam from config.
type FeatureID string

const (
	F_RHYTHM         FeatureID = "rhythm"
	F_THROUGHPUT     FeatureID = "throughput"
	F_NIGHT          FeatureID = "night"
	F_CONTINUITY     FeatureID = "continuity"
	F_CHURN          FeatureID = "churn"
	F_IDENTITY       FeatureID = "identity"
	F_IPREP          FeatureID = "iprep"
	F_PROTOERR       FeatureID = "protoerr"
	F_HTTP_RATE      FeatureID = "http_rate"
	F_HTTP_ERROR     FeatureID = "http_error"
	F_ENDPOINT_FOCUS FeatureID = "endpoint_focus"
	F_AUTH_PROBE     FeatureID = "auth_probe"
	F_ENVELOPE       FeatureID = "envelope"
)

// Err-family prefixes. The server tags anomaly codes with one of these;
// the profile only stores the code string.
const (
	ErrProtoPrefix = "proto:"
	ErrAuthPrefix  = "auth:"
	ErrEnvPrefix   = "env:"
)

// FeatureParam carries one feature's config: weight (0 disables),
// sample floor, ramp bounds and direction.
type FeatureParam struct {
	Weight  float64
	NMin    int
	Lo      float64
	Hi      float64
	HighBad bool
}

// FeatureCtx carries scoring-time context: the clock, location for the
// night window, sliding windows and identity mapping.
type FeatureCtx struct {
	Now           time.Time
	Loc           *time.Location
	ShortWindow   time.Duration
	LongWindow    time.Duration
	GapWindow     time.Duration
	NightStart    int
	NightEnd      int
	IdentityGuest float64
	IdentityTrip  float64
	IdentityKey   float64
}

// Ramp normalizes x into [0,1]: HighBad ramps up from Lo to Hi, !HighBad
// ramps down. A degenerate Lo>=Hi collapses to a step at Hi.
func Ramp(x, lo, hi float64, highBad bool) float64 {
	if hi <= lo {
		if highBad {
			if x < hi {
				return 0
			}
			return 1
		}
		if x < hi {
			return 1
		}
		return 0
	}
	if highBad {
		return clamp01((x - lo) / (hi - lo))
	}
	return clamp01((hi - x) / (hi - lo))
}

func clamp01(x float64) float64 {
	if x < 0 {
		return 0
	}
	if x > 1 {
		return 1
	}
	return x
}

func since(ts []time.Time, cut time.Time) []time.Time {
	var kept []time.Time
	for _, t := range ts {
		if !t.Before(cut) {
			kept = append(kept, t)
		}
	}
	return kept
}

// intervalsCV returns the coefficient of variation of inter-arrival
// gaps: near 0 for metronome traffic, ~1+ for bursty human traffic.
func intervalsCV(ts []time.Time) (float64, bool) {
	if len(ts) < 3 {
		return 0, false
	}
	var sum, sum2 float64
	n := 0
	for i := 1; i < len(ts); i++ {
		d := ts[i].Sub(ts[i-1]).Seconds()
		if d < 0 {
			continue
		}
		sum += d
		sum2 += d * d
		n++
	}
	if n < 2 || sum <= 0 {
		return 0, false
	}
	mean := sum / float64(n)
	variance := sum2/float64(n) - mean*mean
	if variance <= 0 {
		return 0, true
	}
	return math.Sqrt(variance) / mean, true
}

func inNight(t time.Time, loc *time.Location, start, end int) bool {
	if loc == nil {
		loc = time.UTC
	}
	h := t.In(loc).Hour()
	if start <= end {
		return h >= start && h < end
	}
	return h >= start || h < end
}

// longestRun returns the longest span (hours) covered by events whose
// consecutive gaps stay within gap.
func longestRun(ts []time.Time, gap time.Duration) float64 {
	if len(ts) == 0 {
		return 0
	}
	best := 0.0
	anchor := ts[0]
	prev := ts[0]
	for _, t := range ts[1:] {
		if t.Sub(prev) > gap {
			if d := prev.Sub(anchor).Hours(); d > best {
				best = d
			}
			anchor = t
		}
		prev = t
	}
	if d := prev.Sub(anchor).Hours(); d > best {
		best = d
	}
	return best
}

func countErrPrefix(errs []ErrEvent, cut time.Time, prefix string) int {
	n := 0
	for _, e := range errs {
		if e.Time.Before(cut) {
			continue
		}
		if strings.HasPrefix(e.Code, prefix) {
			n++
		}
	}
	return n
}

// FeatureValue computes one normalized anomaly in [0,1]. False means
// insufficient sample: the caller must exclude the feature (and its
// weight) from the aggregate.
func FeatureValue(p *Profile, id FeatureID, fp FeatureParam, ctx FeatureCtx) (float64, bool) {
	now := ctx.Now
	switch id {
	case F_RHYTHM:
		msgs := since(p.Msgs, now.Add(-ctx.LongWindow))
		if len(msgs) < fp.NMin {
			return 0, false
		}
		cv, ok := intervalsCV(msgs)
		if !ok {
			return 0, false
		}
		return Ramp(cv, fp.Lo, fp.Hi, false), true
	case F_THROUGHPUT:
		msgs := since(p.Msgs, now.Add(-ctx.LongWindow))
		if len(msgs) < fp.NMin {
			return 0, false
		}
		rate := float64(len(msgs)) / ctx.LongWindow.Hours()
		return Ramp(rate, fp.Lo, fp.Hi, true), true
	case F_NIGHT:
		msgs := since(p.Msgs, now.Add(-ctx.LongWindow))
		if len(msgs) < fp.NMin {
			return 0, false
		}
		night := 0
		for _, t := range msgs {
			if inNight(t, ctx.Loc, ctx.NightStart, ctx.NightEnd) {
				night++
			}
		}
		return Ramp(float64(night)/float64(len(msgs)), fp.Lo, fp.Hi, true), true
	case F_CONTINUITY:
		if len(p.Msgs) < fp.NMin {
			return 0, false
		}
		return Ramp(longestRun(p.Msgs, ctx.GapWindow), fp.Lo, fp.Hi, true), true
	case F_CHURN:
		conns := since(p.Conns, now.Add(-ctx.LongWindow))
		if len(conns) < fp.NMin {
			return 0, false
		}
		rate := float64(len(conns)) / ctx.LongWindow.Hours()
		return Ramp(rate, fp.Lo, fp.Hi, true), true
	case F_IDENTITY:
		switch p.Identity {
		case IdKey:
			return ctx.IdentityKey, true
		case IdTrip:
			return ctx.IdentityTrip, true
		default:
			return ctx.IdentityGuest, true
		}
	case F_IPREP:
		if p.Blocklisted {
			return 1, true
		}
		if p.IdentityCount < fp.NMin {
			return 0, false
		}
		return Ramp(float64(p.IdentityCount), fp.Lo, fp.Hi, true), true
	case F_PROTOERR:
		n := countErrPrefix(p.Errs, now.Add(-ctx.LongWindow), ErrProtoPrefix)
		if n < fp.NMin {
			return 0, false
		}
		return Ramp(float64(n), fp.Lo, fp.Hi, true), true
	case F_AUTH_PROBE:
		n := countErrPrefix(p.Errs, now.Add(-ctx.LongWindow), ErrAuthPrefix)
		if n < fp.NMin {
			return 0, false
		}
		return Ramp(float64(n), fp.Lo, fp.Hi, true), true
	case F_ENVELOPE:
		n := countErrPrefix(p.Errs, now.Add(-ctx.LongWindow), ErrEnvPrefix)
		if n < fp.NMin {
			return 0, false
		}
		return Ramp(float64(n), fp.Lo, fp.Hi, true), true
	case F_HTTP_RATE:
		var inWin int
		for _, e := range p.HTTP {
			if !e.Time.Before(now.Add(-ctx.ShortWindow)) {
				inWin++
			}
		}
		if inWin < fp.NMin {
			return 0, false
		}
		rate := float64(inWin) / ctx.ShortWindow.Minutes()
		return Ramp(rate, fp.Lo, fp.Hi, true), true
	case F_HTTP_ERROR:
		var total, bad int
		for _, e := range p.HTTP {
			if e.Time.Before(now.Add(-ctx.ShortWindow)) {
				continue
			}
			total++
			if e.Status >= 400 {
				bad++
			}
		}
		if total < fp.NMin {
			return 0, false
		}
		return Ramp(float64(bad)/float64(total), fp.Lo, fp.Hi, true), true
	case F_ENDPOINT_FOCUS:
		counts := map[string]int{}
		total := 0
		for _, e := range p.HTTP {
			if e.Time.Before(now.Add(-ctx.ShortWindow)) {
				continue
			}
			counts[e.Class]++
			total++
		}
		if total < fp.NMin {
			return 0, false
		}
		peak := 0
		for _, n := range counts {
			if n > peak {
				peak = n
			}
		}
		return Ramp(float64(peak)/float64(total), fp.Lo, fp.Hi, true), true
	}
	return 0, false
}

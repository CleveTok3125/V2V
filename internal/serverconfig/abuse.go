package serverconfig

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/CleveTok3125/V2V/internal/behavior"
)

// PowTier is one configured PoW tier. Memory is KiB, matching x/crypto
// and the tripcode presets. Tier 0 is the no-PoW tier and carries no
// preset: only tiers 1..count-1 are read.
type PowTier struct {
	T          int
	M          int
	P          int
	Difficulty uint
	EstMs      int
}

// AbuseConfig holds the always-required abuse knobs (group A): global
// cap, under-attack, PoW ladder and gate. Behavior is nil when
// BEHAVIOR_ENABLED=false, in which case no group-B key is needed.
type AbuseConfig struct {
	MaxTotalConnections int
	AttackEnterSecs     time.Duration
	AttackEnterRPS      int
	UnderAttackTTL      time.Duration
	UnderAttackForce    string
	PowFirstConnect     string
	PowGateTier         int
	PowTierCount        int
	PowTiers            []PowTier
	PowTTL              time.Duration
	JoinWaitMin         time.Duration
	JoinWaitMax         time.Duration
	ScreenDeadline      time.Duration
	GateHTTPEnabled     bool
	GateHTTPClasses     []string
	GateHTTPMode        string
	GateHTTPTierMin     int
	PassTTL             time.Duration
	PassSingleUse       bool
	Behavior            *BehaviorConfig
}

// BehaviorConfig holds the group-B knobs, required only when the
// behavior engine is enabled: curve, per-feature params, grouping,
// tier thresholds, re-check cadence, attack scale, retention, stats.
type BehaviorConfig struct {
	CurveGamma   float64
	WindowShort  time.Duration
	WindowLong   time.Duration
	GapWindow    time.Duration
	NightStart   int
	NightEnd     int
	IdentityVals map[string]float64
	Features     map[behavior.FeatureID]behavior.FeatureParam
	GroupBetaIP  float64
	GroupBeta48  float64
	GroupBetaASN float64
	GroupBetaCT  float64
	GroupMinMemb int
	TierEnter    []float64
	TierExit     []float64
	RecheckMin   time.Duration
	RecheckMax   time.Duration
	// ScoreEveryNMsgs re-arms scoring after this many messages since
	// the last score, so short bursty sessions are re-scored instead
	// of evaluated once at connect time.
	ScoreEveryNMsgs int
	ScaleMode       string
	ScaleW          [4]float64
	BumpMax         int
	Retention       []time.Duration
	StatsEnabled    bool
	StatsWindow     time.Duration
}

var abuseFeatures = []struct {
	id  behavior.FeatureID
	env string
}{
	{behavior.F_RHYTHM, "RHYTHM"},
	{behavior.F_THROUGHPUT, "THROUGHPUT"},
	{behavior.F_NIGHT, "NIGHT"},
	{behavior.F_CONTINUITY, "CONTINUITY"},
	{behavior.F_CHURN, "CHURN"},
	{behavior.F_IDENTITY, "IDENTITY"},
	{behavior.F_IPREP, "IPREP"},
	{behavior.F_PROTOERR, "PROTOERR"},
	{behavior.F_HTTP_RATE, "HTTP_RATE"},
	{behavior.F_HTTP_ERROR, "HTTP_ERROR"},
	{behavior.F_ENDPOINT_FOCUS, "ENDPOINT_FOCUS"},
	{behavior.F_AUTH_PROBE, "AUTH_PROBE"},
	{behavior.F_ENVELOPE, "ENVELOPE"},
}

// EffectiveScaleMode returns the attack-scale mode, defaulting to max
// when behavior scoring is off.
func (c *AbuseConfig) EffectiveScaleMode() string {
	if c != nil && c.Behavior != nil {
		return c.Behavior.ScaleMode
	}
	return "max"
}

// EffectiveScaleW returns the attack-scale weights, defaulting to
// uniform when behavior scoring is off.
func (c *AbuseConfig) EffectiveScaleW() [4]float64 {
	if c != nil && c.Behavior != nil {
		return c.Behavior.ScaleW
	}
	return [4]float64{1, 1, 1, 1}
}

// abuseLoader accumulates every missing/malformed key so one validate
// run names them all instead of failing on the first.
type abuseLoader struct {
	missing []string
	bad     []string
}

func (l *abuseLoader) raw(key string) (string, bool) {
	v, ok := os.LookupEnv(key)
	if !ok || strings.TrimSpace(v) == "" {
		l.missing = append(l.missing, key)
		return "", false
	}
	return strings.TrimSpace(v), true
}

func (l *abuseLoader) reqInt(key string) (int, bool) {
	v, ok := l.raw(key)
	if !ok {
		return 0, false
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		l.bad = append(l.bad, fmt.Sprintf("%s: %v", key, err))
		return 0, false
	}
	return n, true
}

func (l *abuseLoader) reqDuration(key string) (time.Duration, bool) {
	v, ok := l.raw(key)
	if !ok {
		return 0, false
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		l.bad = append(l.bad, fmt.Sprintf("%s: %v", key, err))
		return 0, false
	}
	return d, true
}

func (l *abuseLoader) reqFloat(key string) (float64, bool) {
	v, ok := l.raw(key)
	if !ok {
		return 0, false
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		l.bad = append(l.bad, fmt.Sprintf("%s: %v", key, err))
		return 0, false
	}
	return f, true
}

func (l *abuseLoader) reqBool(key string) (bool, bool) {
	v, ok := l.raw(key)
	if !ok {
		return false, false
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		l.bad = append(l.bad, fmt.Sprintf("%s: %v", key, err))
		return false, false
	}
	return b, true
}

func (l *abuseLoader) reqEnum(key string, allowed ...string) (string, bool) {
	v, ok := l.raw(key)
	if !ok {
		return "", false
	}
	for _, a := range allowed {
		if v == a {
			return v, true
		}
	}
	l.bad = append(l.bad, fmt.Sprintf("%s: must be one of %s", key, strings.Join(allowed, "|")))
	return "", false
}

func (l *abuseLoader) err() error {
	if len(l.missing) == 0 && len(l.bad) == 0 {
		return nil
	}
	var parts []string
	for _, k := range l.missing {
		parts = append(parts, "missing "+k)
	}
	parts = append(parts, l.bad...)
	return fmt.Errorf("abuse config: %s", strings.Join(parts, "; "))
}

// behaviorEnabled reads the master toggle, defaulting to true (the
// template declares it; a missing key keeps the engine on rather than
// silently disabling protection).
func behaviorEnabled(l *abuseLoader) bool {
	v, ok := os.LookupEnv("BEHAVIOR_ENABLED")
	if !ok || strings.TrimSpace(v) == "" {
		return true
	}
	b, err := strconv.ParseBool(strings.TrimSpace(v))
	if err != nil {
		l.bad = append(l.bad, fmt.Sprintf("BEHAVIOR_ENABLED: %v", err))
		return true
	}
	return b
}

// LoadAbuseConfig reads group A always and group B when the behavior
// engine is enabled. Missing or malformed keys fail with every
// offender named; there are no silent fallbacks.
func LoadAbuseConfig() (AbuseConfig, []string, error) {
	l := &abuseLoader{}
	var cfg AbuseConfig

	cfg.MaxTotalConnections, _ = l.reqInt("MAX_TOTAL_CONNECTIONS")
	cfg.AttackEnterSecs, _ = l.reqDuration("ATTACK_ENTER_SECS")
	cfg.AttackEnterRPS, _ = l.reqInt("ATTACK_ENTER_RPS")
	cfg.UnderAttackTTL, _ = l.reqDuration("UNDER_ATTACK_TTL")
	cfg.UnderAttackForce, _ = l.reqEnum("UNDER_ATTACK_FORCE", "auto", "on", "off")
	cfg.PowFirstConnect, _ = l.reqEnum("POW_FIRST_CONNECT", "never", "always", "under-attack")
	cfg.PowGateTier, _ = l.reqInt("POW_GATE_TIER")
	cfg.PowTierCount, _ = l.reqInt("POW_TIER_COUNT")
	cfg.PowTTL, _ = l.reqDuration("POW_TTL")
	cfg.JoinWaitMin, _ = l.reqDuration("JOIN_WAIT_MIN")
	cfg.JoinWaitMax, _ = l.reqDuration("JOIN_WAIT_MAX")
	cfg.ScreenDeadline, _ = l.reqDuration("SCREEN_DEADLINE")
	cfg.GateHTTPEnabled, _ = l.reqBool("GATE_HTTP_ENABLED")
	cfg.GateHTTPMode, _ = l.reqEnum("GATE_HTTP_MODE", "under-attack", "tier")
	cfg.PassTTL, _ = l.reqDuration("PASS_TTL")
	cfg.PassSingleUse, _ = l.reqBool("PASS_SINGLE_USE")

	if err := l.err(); err != nil {
		return AbuseConfig{}, nil, err
	}
	// Group-A ranges: a zero/negative cap would silently disable it and
	// a zero enter window would pin under-attack on from the first tick.
	if cfg.MaxTotalConnections < 1 {
		return AbuseConfig{}, nil, fmt.Errorf("abuse config: MAX_TOTAL_CONNECTIONS must be >= 1")
	}
	if cfg.AttackEnterSecs <= 0 {
		return AbuseConfig{}, nil, fmt.Errorf("abuse config: ATTACK_ENTER_SECS must be positive")
	}
	if cfg.AttackEnterRPS < 1 {
		return AbuseConfig{}, nil, fmt.Errorf("abuse config: ATTACK_ENTER_RPS must be >= 1")
	}
	if cfg.PowTierCount < 2 {
		return AbuseConfig{}, nil, fmt.Errorf("abuse config: POW_TIER_COUNT must be >= 2")
	}
	if cfg.PowGateTier < 0 || cfg.PowGateTier >= cfg.PowTierCount {
		return AbuseConfig{}, nil, fmt.Errorf("abuse config: POW_GATE_TIER out of range")
	}
	cfg.PowTiers = make([]PowTier, cfg.PowTierCount)
	for n := 1; n < cfg.PowTierCount; n++ {
		p := fmt.Sprintf("POW_P%d_", n)
		t, _ := l.reqInt(p + "T")
		m, _ := l.reqInt(p + "M")
		pp, _ := l.reqInt(p + "P")
		d, _ := l.reqInt(p + "DIFF")
		e, _ := l.reqInt(p + "EST_MS")
		// Negative difficulty would wrap to a huge uint and is caught
		// by the >64 range check below, so a typo fails the boot
		// instead of silently disabling PoW.
		cfg.PowTiers[n] = PowTier{T: t, M: m, P: pp, Difficulty: uint(d), EstMs: e}
	}
	if err := l.err(); err != nil {
		return AbuseConfig{}, nil, err
	}
	for n := 1; n < cfg.PowTierCount; n++ {
		pt := cfg.PowTiers[n]
		if pt.T < 1 || pt.T > 10 || pt.M < 8*1024 || pt.M > 256*1024 || pt.P < 1 || pt.P > 8 || pt.Difficulty > 64 || pt.EstMs <= 0 {
			return AbuseConfig{}, nil, fmt.Errorf("abuse config: POW_P%d_* out of range", n)
		}
	}
	if cfg.JoinWaitMin <= 0 || cfg.JoinWaitMax < cfg.JoinWaitMin {
		return AbuseConfig{}, nil, fmt.Errorf("abuse config: JOIN_WAIT_MIN/MAX must satisfy 0<MIN<=MAX")
	}
	if cfg.ScreenDeadline <= 0 || cfg.PowTTL <= 0 || cfg.PassTTL <= 0 {
		return AbuseConfig{}, nil, fmt.Errorf("abuse config: SCREEN_DEADLINE/POW_TTL/PASS_TTL must be positive")
	}
	if cfg.GateHTTPEnabled {
		raw, ok := l.raw("GATE_HTTP_CLASSES")
		if !ok {
			return AbuseConfig{}, nil, fmt.Errorf("abuse config: missing GATE_HTTP_CLASSES")
		}
		for _, c := range strings.Split(raw, ",") {
			if c = strings.TrimSpace(c); c != "" {
				cfg.GateHTTPClasses = append(cfg.GateHTTPClasses, c)
			}
		}
		if len(cfg.GateHTTPClasses) == 0 {
			return AbuseConfig{}, nil, fmt.Errorf("abuse config: GATE_HTTP_CLASSES is empty")
		}
		cfg.GateHTTPTierMin, _ = l.reqInt("GATE_HTTP_TIER_MIN")
		if err := l.err(); err != nil {
			return AbuseConfig{}, nil, err
		}
		if cfg.GateHTTPTierMin < 1 {
			return AbuseConfig{}, nil, fmt.Errorf("abuse config: GATE_HTTP_TIER_MIN must be >= 1")
		}
	}

	if !behaviorEnabled(l) {
		if err := l.err(); err != nil {
			return AbuseConfig{}, nil, err
		}
		return cfg, nil, nil
	}
	b := &BehaviorConfig{}
	b.CurveGamma, _ = l.reqFloat("BEHAVIOR_CURVE_GAMMA")
	b.WindowShort, _ = l.reqDuration("BEHAVIOR_WINDOW_SHORT")
	b.WindowLong, _ = l.reqDuration("BEHAVIOR_WINDOW_LONG")
	b.GapWindow, _ = l.reqDuration("BEHAVIOR_GAP_WINDOW")
	b.NightStart, _ = l.reqInt("BEHAVIOR_NIGHT_START")
	b.NightEnd, _ = l.reqInt("BEHAVIOR_NIGHT_END")
	g, _ := l.reqFloat("BEHAVIOR_IDENTITY_GUEST")
	tr, _ := l.reqFloat("BEHAVIOR_IDENTITY_TRIP")
	k, _ := l.reqFloat("BEHAVIOR_IDENTITY_KEY")
	b.IdentityVals = map[string]float64{"guest": g, "trip": tr, "key": k}
	b.Features = map[behavior.FeatureID]behavior.FeatureParam{}
	for _, f := range abuseFeatures {
		w, _ := l.reqFloat("BEHAVIOR_W_" + f.env)
		nm, _ := l.reqInt("BEHAVIOR_NMIN_" + f.env)
		lo, _ := l.reqFloat("BEHAVIOR_RAMP_" + f.env + "_LO")
		hi, _ := l.reqFloat("BEHAVIOR_RAMP_" + f.env + "_HI")
		b.Features[f.id] = behavior.FeatureParam{Weight: w, NMin: nm, Lo: lo, Hi: hi, HighBad: f.id != behavior.F_RHYTHM}
	}
	b.GroupBetaIP, _ = l.reqFloat("BEHAVIOR_GROUP_BETA_IP")
	b.GroupBeta48, _ = l.reqFloat("BEHAVIOR_GROUP_BETA_48")
	b.GroupBetaASN, _ = l.reqFloat("BEHAVIOR_GROUP_BETA_ASN")
	b.GroupBetaCT, _ = l.reqFloat("BEHAVIOR_GROUP_BETA_COUNTRY")
	b.GroupMinMemb, _ = l.reqInt("BEHAVIOR_GROUP_MIN_MEMBERS")
	var enterVals, exitVals []float64
	for n := 1; n < cfg.PowTierCount; n++ {
		en, _ := l.reqFloat(fmt.Sprintf("BEHAVIOR_TIER%d_ENTER", n))
		ex, _ := l.reqFloat(fmt.Sprintf("BEHAVIOR_TIER%d_EXIT", n))
		enterVals = append(enterVals, en)
		exitVals = append(exitVals, ex)
	}
	b.TierEnter = append([]float64{0}, enterVals...)
	b.TierExit = append([]float64{0}, exitVals...)
	b.RecheckMin, _ = l.reqDuration("POW_RECHECK_MIN")
	b.RecheckMax, _ = l.reqDuration("POW_RECHECK_MAX")
	b.ScoreEveryNMsgs, _ = l.reqInt("BEHAVIOR_SCORE_EVERY_N_MSGS")
	b.ScaleMode, _ = l.reqEnum("ATTACK_SCALE_MODE", "max", "weighted")
	b.ScaleW[0], _ = l.reqFloat("ATTACK_SCALE_W_REJECT")
	b.ScaleW[1], _ = l.reqFloat("ATTACK_SCALE_W_CONNRATE")
	b.ScaleW[2], _ = l.reqFloat("ATTACK_SCALE_W_IPGROWTH")
	b.ScaleW[3], _ = l.reqFloat("ATTACK_SCALE_W_BLOCKLIST")
	b.BumpMax, _ = l.reqInt("ATTACK_TIER_BUMP_MAX")
	b.Retention = make([]time.Duration, cfg.PowTierCount)
	for n := 0; n < cfg.PowTierCount; n++ {
		b.Retention[n], _ = l.reqDuration(fmt.Sprintf("BEHAVIOR_RETENTION_P%d", n))
	}
	b.StatsEnabled, _ = l.reqBool("BEHAVIOR_STATS_ENABLED")
	b.StatsWindow, _ = l.reqDuration("BEHAVIOR_STATS_WINDOW")
	if err := l.err(); err != nil {
		return AbuseConfig{}, nil, err
	}

	if b.CurveGamma <= 0 {
		return AbuseConfig{}, nil, fmt.Errorf("abuse config: BEHAVIOR_CURVE_GAMMA must be positive")
	}
	if b.WindowShort <= 0 || b.WindowLong <= 0 || b.GapWindow <= 0 {
		return AbuseConfig{}, nil, fmt.Errorf("abuse config: behavior windows must be positive")
	}
	if b.NightStart < 0 || b.NightStart > 23 || b.NightEnd < 0 || b.NightEnd > 23 {
		return AbuseConfig{}, nil, fmt.Errorf("abuse config: night hours must be 0-23")
	}
	for name, v := range b.IdentityVals {
		if v < 0 || v > 1 {
			return AbuseConfig{}, nil, fmt.Errorf("abuse config: BEHAVIOR_IDENTITY_%s must be 0-1", strings.ToUpper(name))
		}
	}
	for _, f := range abuseFeatures {
		fp := b.Features[f.id]
		if fp.Weight < 0 || fp.NMin < 0 || fp.Lo >= fp.Hi {
			return AbuseConfig{}, nil, fmt.Errorf("abuse config: BEHAVIOR_*_%s invalid (weight>=0, nmin>=0, lo<hi)", f.env)
		}
	}
	for _, v := range []float64{b.GroupBetaIP, b.GroupBeta48, b.GroupBetaASN, b.GroupBetaCT} {
		if v < 0 {
			return AbuseConfig{}, nil, fmt.Errorf("abuse config: group beta must be >= 0")
		}
	}
	if b.GroupMinMemb < 1 {
		return AbuseConfig{}, nil, fmt.Errorf("abuse config: BEHAVIOR_GROUP_MIN_MEMBERS must be >= 1")
	}
	for n := 1; n < cfg.PowTierCount; n++ {
		en, ex := b.TierEnter[n], b.TierExit[n]
		if en < 0 || en > 1 || ex < 0 || ex > 1 {
			return AbuseConfig{}, nil, fmt.Errorf("abuse config: tier %d thresholds must be 0-1", n)
		}
		prevEnter := 0.0
		if n > 1 {
			prevEnter = b.TierEnter[n-1]
		}
		if !(en > ex && ex > prevEnter) {
			return AbuseConfig{}, nil, fmt.Errorf("abuse config: tier %d must satisfy ENTER>EXIT>prevENTER", n)
		}
	}
	if b.RecheckMin <= 0 || b.RecheckMax < b.RecheckMin {
		return AbuseConfig{}, nil, fmt.Errorf("abuse config: POW_RECHECK_MIN/MAX must satisfy 0<MIN<=MAX")
	}
	if b.ScoreEveryNMsgs < 1 {
		return AbuseConfig{}, nil, fmt.Errorf("abuse config: BEHAVIOR_SCORE_EVERY_N_MSGS must be >= 1")
	}
	if b.BumpMax < 0 || b.BumpMax >= cfg.PowTierCount {
		return AbuseConfig{}, nil, fmt.Errorf("abuse config: ATTACK_TIER_BUMP_MAX out of range")
	}
	for n, r := range b.Retention {
		if r <= 0 {
			return AbuseConfig{}, nil, fmt.Errorf("abuse config: BEHAVIOR_RETENTION_P%d must be positive", n)
		}
	}
	if b.StatsWindow <= 0 {
		return AbuseConfig{}, nil, fmt.Errorf("abuse config: BEHAVIOR_STATS_WINDOW must be positive")
	}
	cfg.Behavior = b
	return cfg, nil, nil
}

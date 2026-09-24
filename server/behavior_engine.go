package main

import (
	"strings"
	"sync"
	"time"

	"github.com/CleveTok3125/V2V/internal/behavior"
	"github.com/CleveTok3125/V2V/internal/serverconfig"
)

// BehaviorEngine scores IPs from the behavior store: feature values,
// weighted aggregate, group combine and hysteresis tiers. Identity
// alone never condemns: with no evidence feature valid, a clean IP
// scores 0 (cold start stays tier 0) and only a blocklist hit scores 1.
type BehaviorEngine struct {
	mu       sync.Mutex
	store    *behavior.Store
	stats    *behavior.Stats
	geo      behavior.Resolver
	scores   map[string]float64
	members  map[string]map[string]bool
	ipGroups map[string][]string
	names    map[string]map[string]bool
	file     string
}

// NewBehaviorEngine wires store, stats, geo resolver and file path.
// Any of them may be nil/empty to disable that leg (tests).
func NewBehaviorEngine(store *behavior.Store, stats *behavior.Stats, geo behavior.Resolver, file string) *BehaviorEngine {
	return &BehaviorEngine{
		store:    store,
		stats:    stats,
		geo:      geo,
		scores:   map[string]float64{},
		members:  map[string]map[string]bool{},
		ipGroups: map[string][]string{},
		names:    map[string]map[string]bool{},
		file:     file,
	}
}

func (e *BehaviorEngine) profile(ip string) *behavior.Profile {
	return e.store.Get(ip)
}

// ObserveConnect records a handshake completion with display name and
// identity kind.
func (e *BehaviorEngine) ObserveConnect(ip, name string, kind behavior.IdentityKind, now time.Time) {
	e.mu.Lock()
	defer e.mu.Unlock()
	p := e.store.Get(ip)
	p.AddConnect(now)
	p.SetIdentity(kind)
	set := e.names[ip]
	if set == nil {
		set = map[string]bool{}
		e.names[ip] = set
	}
	if len(set) < 64 {
		set[name] = true
	}
	p.IdentityCount = len(set)
}

// ObserveMessage records one accepted chat message (time only).
func (e *BehaviorEngine) ObserveMessage(ip string, now time.Time) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.store.Get(ip).AddMessage(now)
}

// ObserveHistory records one on-demand history request (time only).
func (e *BehaviorEngine) ObserveHistory(ip string, now time.Time) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.store.Get(ip).AddHistoryReq(now)
}

// ObserveHTTP records one non-chat request (class/status/time only).
func (e *BehaviorEngine) ObserveHTTP(ip, class string, status int, now time.Time) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.store.Get(ip).AddHTTP(behavior.HTTPEvent{Time: now, Class: class, Status: status})
}

// ObserveErr records one auth/protocol anomaly code (time only).
func (e *BehaviorEngine) ObserveErr(ip, code string, now time.Time) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.store.Get(ip).AddErr(behavior.ErrEvent{Time: now, Code: code})
}

// ObserveDisconnect touches LastSeen so idle retention is honest.
func (e *BehaviorEngine) ObserveDisconnect(ip string, now time.Time) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.store.Get(ip).AddDisconnect(now)
}

// groupBeta picks the configured weight by group-key prefix.
func groupBeta(key string, bc *serverconfig.BehaviorConfig) float64 {
	switch {
	case strings.HasPrefix(key, "ip6/48:"):
		return bc.GroupBeta48
	case strings.HasPrefix(key, "asn:"):
		return bc.GroupBetaASN
	case strings.HasPrefix(key, "country:"), strings.HasPrefix(key, "region:"):
		return bc.GroupBetaCT
	default:
		return bc.GroupBetaIP
	}
}

// Score computes the current tier and suspicion for ip. Pure scoring:
// no challenges, no side effects beyond index updates.
func (e *BehaviorEngine) Score(ip string, bc *serverconfig.BehaviorConfig, now time.Time, loc *time.Location) (int, float64) {
	e.mu.Lock()
	defer e.mu.Unlock()
	p := e.store.Get(ip)
	ctx := behavior.FeatureCtx{
		Now: now, Loc: loc,
		ShortWindow: bc.WindowShort, LongWindow: bc.WindowLong, GapWindow: bc.GapWindow,
		NightStart: bc.NightStart, NightEnd: bc.NightEnd,
		IdentityGuest: bc.IdentityVals["guest"],
		IdentityTrip:  bc.IdentityVals["trip"],
		IdentityKey:   bc.IdentityVals["key"],
	}
	vals := map[behavior.FeatureID]float64{}
	hasEvidence := false
	for id, fp := range bc.Features {
		if fp.Weight <= 0 {
			continue
		}
		v, ok := behavior.FeatureValue(p, id, fp, ctx)
		if !ok {
			continue
		}
		vals[id] = v
		if id != behavior.F_IDENTITY {
			hasEvidence = true
		}
	}
	var own float64
	if !hasEvidence {
		if p.Blocklisted {
			own = 1
		}
	} else {
		fallback := 0.0
		if p.Blocklisted {
			fallback = 1
		}
		own = behavior.Aggregate(vals, bc.Features, bc.CurveGamma, fallback)
	}

	keys := append(behavior.GroupKeys(ip), behavior.GeoKeys(e.geo, ip)...)
	e.ipGroups[ip] = keys
	for _, k := range keys {
		set := e.members[k]
		if set == nil {
			set = map[string]bool{}
			e.members[k] = set
		}
		set[ip] = true
	}
	s := own
	for _, k := range keys {
		set := e.members[k]
		if len(set) < bc.GroupMinMemb {
			continue
		}
		var memberScores []float64
		for m := range set {
			if ms, ok := e.scores[m]; ok {
				memberScores = append(memberScores, ms)
			}
		}
		if len(memberScores) == 0 {
			continue
		}
		if g := behavior.GroupAggregate(memberScores, groupBeta(k, bc)); g > s {
			s = g
		}
	}

	maxTier := len(bc.TierEnter) - 1
	tier := behavior.NextTier(s, p.Tier, bc.TierEnter, bc.TierExit, maxTier)
	p.Tier = tier
	e.scores[ip] = s
	if e.stats != nil {
		e.stats.Observe(tier)
	}
	return tier, s
}

// Prune drops idle profiles past tier retention and cleans every
// index entry that belonged to them. Survivors keep their scores,
// groups and names.
func (e *BehaviorEngine) Prune(now time.Time, retention map[int]time.Duration, def time.Duration) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, ip := range e.store.Prune(now, retention, def) {
		delete(e.scores, ip)
		delete(e.names, ip)
		for _, k := range e.ipGroups[ip] {
			if set := e.members[k]; set != nil {
				delete(set, ip)
				if len(set) == 0 {
					delete(e.members, k)
				}
			}
		}
		delete(e.ipGroups, ip)
	}
}

// Save persists profiles; Load restores them. Empty path disables.
func (e *BehaviorEngine) Save() error {
	if e.file == "" {
		return nil
	}
	return e.store.Save(e.file)
}

// Load restores profiles. Empty path or missing file disables.
func (e *BehaviorEngine) Load() error {
	if e.file == "" {
		return nil
	}
	return e.store.Load(e.file)
}

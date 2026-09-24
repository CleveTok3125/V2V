package main

import (
	"sync"
	"time"

	"github.com/CleveTok3125/V2V/internal/behavior"
)

// Tunables below are internal estimator constants, not operator policy:
// the policy (thresholds, TTL, weights) rides in AbuseConfig.
const (
	attackWindowSecs = 5
	attackBuckets    = 8
	attackSeenCap    = 20000
	attackSeenTTL    = 300
	attackBaselineA  = 0.05
)

// secBucket counts one wall-clock second of handshake outcomes.
type secBucket struct {
	sec    int64
	att    int
	r503   int
	rGate  int
	rBlock int
	newIP  int
}

// SampleParams carries the abuse policy for one sampler tick.
type SampleParams struct {
	EnterSecs int
	EnterRPS  int
	TTL       time.Duration
	Force     string
	ScaleMode string
	ScaleW    [4]float64
}

// SampleState is the sampler outcome for one tick.
type SampleState struct {
	Under bool
	Scale float64
}

// AttackState tracks handshake pressure and owns the under-attack
// flag: enter on sustained full cap or reject burst, hold the full
// TTL with re-arm while hot, exit only after a quiet TTL.
type AttackState struct {
	mu      sync.Mutex
	under   bool
	until   time.Time
	entered time.Time
	run     int
	buckets [attackBuckets]secBucket
	seen    map[string]int64
	baseAtt float64
	baseIP  float64
	scale   float64
}

// NewAttackState creates an idle tracker.
func NewAttackState() *AttackState {
	return &AttackState{seen: map[string]int64{}}
}

func (a *AttackState) bucket(sec int64) *secBucket {
	b := &a.buckets[sec%attackBuckets]
	if b.sec != sec {
		*b = secBucket{sec: sec}
	}
	return b
}

// noteAttempt records one handshake arrival for rate baselines.
func (a *AttackState) noteAttempt(ip string) {
	now := time.Now().Unix()
	a.mu.Lock()
	defer a.mu.Unlock()
	b := a.bucket(now)
	b.att++
	if last, ok := a.seen[ip]; !ok || now-last > attackSeenTTL {
		b.newIP++
		if len(a.seen) < attackSeenCap {
			a.seen[ip] = now
		}
	}
}

func (a *AttackState) note503()   { a.noteReject(0) }
func (a *AttackState) noteGate()  { a.noteReject(1) }
func (a *AttackState) noteBlock() { a.noteReject(2) }

func (a *AttackState) noteReject(which int) {
	now := time.Now().Unix()
	a.mu.Lock()
	defer a.mu.Unlock()
	b := a.bucket(now)
	switch which {
	case 0:
		b.r503++
	case 1:
		b.rGate++
	default:
		b.rBlock++
	}
}

// IsUnderAttack reports the flag with TTL expiry applied.
func (a *AttackState) IsUnderAttack() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.under && time.Now().Before(a.until)
}

// Scale returns the last computed attack magnitude in [0,1].
func (a *AttackState) Scale() float64 {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.scale
}

// sample folds the last attackWindowSecs of buckets, updates the
// enter/exit machine and the rate baselines, and returns the state.
func (a *AttackState) sample(full bool, now time.Time, p SampleParams) SampleState {
	a.mu.Lock()
	defer a.mu.Unlock()

	sec := now.Unix()
	var att, r503, rGate, rBlock, newIP int
	for i := range a.buckets {
		b := &a.buckets[i]
		if b.sec <= sec-attackWindowSecs || b.sec > sec {
			continue
		}
		att += b.att
		r503 += b.r503
		rGate += b.rGate
		rBlock += b.rBlock
		newIP += b.newIP
	}
	rejects := r503 + rGate
	attackSig := full || rejects > p.EnterRPS*attackWindowSecs
	if a.under && p.Force == "off" {
		a.under = false
		a.run = 0
	} else if p.Force == "on" {
		if !a.under {
			a.under = true
			a.entered = now
		}
		a.until = now.Add(p.TTL)
		a.run = 0
	} else {
		if attackSig {
			a.run++
		} else {
			a.run = 0
		}
		if !a.under && a.run >= p.EnterSecs {
			a.under = true
			a.entered = now
			a.until = now.Add(p.TTL)
		}
		if a.under {
			if attackSig {
				a.until = now.Add(p.TTL)
			} else if now.After(a.until) {
				a.under = false
				a.run = 0
			}
		}
	}

	rate := float64(att) / attackWindowSecs
	ipRate := float64(newIP) / attackWindowSecs
	if a.baseAtt == 0 {
		a.baseAtt = rate
	} else {
		a.baseAtt += attackBaselineA * (rate - a.baseAtt)
	}
	if a.baseIP == 0 {
		a.baseIP = ipRate
	} else {
		a.baseIP += attackBaselineA * (ipRate - a.baseIP)
	}
	growth := func(cur, base float64) float64 {
		if cur <= 0 {
			return 0
		}
		g := cur / (base + 1e-9)
		return 1 - 1/(1+g)
	}
	den := float64(att)
	if den < 1 {
		den = 1
	}
	rejectRatio := float64(rejects) / den
	blockRatio := float64(rBlock) / den
	a.scale = behavior.AttackScale(rejectRatio, growth(rate, a.baseAtt), growth(ipRate, a.baseIP), blockRatio, p.ScaleMode, p.ScaleW)
	return SampleState{Under: a.under && now.Before(a.until), Scale: a.scale}
}

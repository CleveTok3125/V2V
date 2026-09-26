package main

import (
	"testing"
	"time"

	"github.com/CleveTok3125/V2V/internal/behavior"
	"github.com/CleveTok3125/V2V/internal/serverconfig"
)

// Short bursty sessions must be re-scored as they chat, not evaluated
// once at connect time: 20 msgs trip every=20, a Score resets it.
func TestEngineDueForRescoreEveryNMsgs(t *testing.T) {
	eng := NewBehaviorEngine(behavior.NewStore(100), behavior.NewStats(), stubGeo{}, "")
	now := time.Now()
	ip := "10.0.0.11"
	const every = 20
	for i := 0; i < every-1; i++ {
		eng.ObserveMessage(ip, now)
	}
	if eng.DueForRescore(ip, every) {
		t.Fatal("19 msgs must not trip every=20")
	}
	eng.ObserveMessage(ip, now)
	if !eng.DueForRescore(ip, every) {
		t.Fatal("20 msgs must trip every=20")
	}
	eng.Score(ip, testBehaviorConfig(), now, time.UTC)
	if eng.DueForRescore(ip, every) {
		t.Fatal("fresh score must reset the counter")
	}
	for i := 0; i < every; i++ {
		eng.ObserveMessage(ip, now)
	}
	if !eng.DueForRescore(ip, every) {
		t.Fatal("20 more msgs must trip again")
	}
}

// observeMessage must flag the IP for the next scheduler tick once the
// message budget is spent.
func TestObserveMessageMarksDue(t *testing.T) {
	eng := NewBehaviorEngine(behavior.NewStore(100), behavior.NewStats(), stubGeo{}, "")
	s := &ChatServer{Behavior: eng, Screener: NewPowScreener(eng)}
	bc := testBehaviorConfig()
	bc.ScoreEveryNMsgs = 20
	old := Cfg.Abuse.Load()
	Cfg.Abuse.Store(&serverconfig.AbuseConfig{Behavior: bc})
	t.Cleanup(func() { Cfg.Abuse.Store(old) })
	now := time.Now()
	ip := "10.0.0.12"
	for i := 0; i < 19; i++ {
		s.observeMessage(ip)
	}
	// Fresh IPs are due until the first tick defers them; defer once
	// to simulate the connect-time score.
	s.Screener.Defer(ip, now.Add(time.Hour))
	if s.Screener.Due(ip, now) {
		t.Fatal("deferred IP must not be due")
	}
	s.observeMessage(ip) // 20th message overall trips every=20.
	// The defer lands at observe time; any later tick sees it due.
	if !s.Screener.Due(ip, now.Add(time.Minute)) {
		t.Fatal("20th msg must mark due for the next tick")
	}
}

// A profile dropped by the store cap resets TotalMsgs while scoredAt
// survives; the stale counter must not suppress the event-driven
// re-score.
func TestDueForRescoreAfterEviction(t *testing.T) {
	eng := NewBehaviorEngine(behavior.NewStore(1), behavior.NewStats(), stubGeo{}, "")
	now := time.Now()
	for i := 0; i < 30; i++ {
		eng.ObserveMessage("10.0.0.20", now)
	}
	eng.Score("10.0.0.20", testBehaviorConfig(), now, time.UTC) // scoredAt=30
	eng.ObserveMessage("10.0.0.99", now)                        // cap=1 evicts .20
	eng.ObserveMessage("10.0.0.20", now)                        // fresh profile
	if !eng.DueForRescore("10.0.0.20", 20) {
		t.Fatal("evicted profile must be due to re-establish its baseline")
	}
}

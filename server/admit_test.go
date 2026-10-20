package main

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/CleveTok3125/V2V/lib/config"
	"github.com/CleveTok3125/V2V/lib/guard"
)

func TestRejectCooldownSaysHowLong(t *testing.T) {
	meter := guard.NewCooldownMap()
	ip := "192.0.2.9"
	if !meter.Allow(ip, 5*time.Second) {
		t.Fatal("first Allow refused")
	}
	rec := httptest.NewRecorder()
	rejectCooldown(rec, meter, ip, 5*time.Second, "too many attempts")
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusTooManyRequests)
	}
	retryAfterSeconds(t, rec)
}

func TestRejectIPBusySaysHowLong(t *testing.T) {
	rec := httptest.NewRecorder()
	rejectIPBusy(rec, "busy")
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusTooManyRequests)
	}
	retryAfterSeconds(t, rec)
}

// TestDegradedNearCap pins the soft threshold at 80% of the hard cap:
// below it the server serves normally, at it history costs double and
// the gate treats the load as an attack.
func TestDegradedNearCap(t *testing.T) {
	old := Cfg.Abuse.Load()
	defer Cfg.Abuse.Store(old)
	Cfg.Abuse.Store(abuseConfigForTest(10))
	s := NewChatServer()
	atomic.StoreInt64(&s.Inflight, 7)
	if s.degraded() {
		t.Fatal("7 of 10 must not be degraded")
	}
	atomic.StoreInt64(&s.Inflight, 8)
	if !s.degraded() {
		t.Fatal("8 of 10 must be degraded")
	}
}

// TestDegradedUnderAttack pins the flag leg: an attack degrades even a
// nearly empty server, and a nil abuse config degrades on the flag alone.
func TestDegradedUnderAttack(t *testing.T) {
	old := Cfg.Abuse.Load()
	defer Cfg.Abuse.Store(old)
	Cfg.Abuse.Store(abuseConfigForTest(1000))
	s := NewChatServer()
	s.Attack.under = true
	s.Attack.until = time.Now().Add(time.Minute)
	if !s.degraded() {
		t.Fatal("under attack must degrade an empty server")
	}
	Cfg.Abuse.Store(nil)
	if !s.degraded() {
		t.Fatal("nil abuse config must still degrade while under attack")
	}
	s.Attack.under = false
	if s.degraded() {
		t.Fatal("quiet server with nil abuse config must not degrade")
	}
}

// TestHistoryBudgetDoublesWhenDegraded pins the only P2 behavior change
// on the hot path: the same cost passes twice when quiet and fails the
// second time when degraded. perSec 0 disables refill for determinism.
func TestHistoryBudgetDoublesWhenDegraded(t *testing.T) {
	testCfg(t)
	old := Cfg.Abuse.Load()
	defer Cfg.Abuse.Store(old)
	cfg := config.DefaultDynamic()
	cfg.HistoryBudgetBurst = 10
	cfg.HistoryBudgetPerSec = 0
	Cfg.Dynamic.Store(cfg)

	Cfg.Abuse.Store(abuseConfigForTest(1000))
	quiet := NewChatServer()
	if !quiet.allowHistoryRequest("10.3.0.1", 4) {
		t.Fatal("first quiet charge must pass")
	}
	if !quiet.allowHistoryRequest("10.3.0.1", 4) {
		t.Fatal("second quiet charge must pass")
	}

	Cfg.Abuse.Store(abuseConfigForTest(10))
	busy := NewChatServer()
	atomic.StoreInt64(&busy.Inflight, 8)
	if !busy.allowHistoryRequest("10.3.0.1", 4) {
		t.Fatal("first degraded charge must pass")
	}
	if busy.allowHistoryRequest("10.3.0.1", 4) {
		t.Fatal("second degraded charge at double cost must be refused")
	}
}

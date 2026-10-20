package main

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/CleveTok3125/V2V/lib/config"
	"github.com/CleveTok3125/V2V/lib/guard"
)

func TestRejectCooldownSaysHowLong(t *testing.T) {
	s := NewChatServer()
	meter := guard.NewCooldownMap()
	ip := "192.0.2.9"
	if !meter.Allow(ip, 5*time.Second) {
		t.Fatal("first Allow refused")
	}
	rec := httptest.NewRecorder()
	s.rejectCooldown(rec, meter, ip, 5*time.Second, "too many attempts")
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusTooManyRequests)
	}
	retryAfterSeconds(t, rec)
	if got := s.loadRejects().Cooldown; got != 1 {
		t.Fatalf("cooldown rejects = %d, want 1", got)
	}
}

func TestRejectIPBusySaysHowLong(t *testing.T) {
	s := NewChatServer()
	rec := httptest.NewRecorder()
	s.rejectIPBusy(rec, "busy")
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusTooManyRequests)
	}
	retryAfterSeconds(t, rec)
	if got := s.loadRejects().PerIP; got != 1 {
		t.Fatalf("perIP rejects = %d, want 1", got)
	}
}

// TestCountRejectTalliesByCause pins the counter mapping: each cause
// lands in its own slot and leaves the others at zero.
func TestCountRejectTalliesByCause(t *testing.T) {
	s := NewChatServer()
	s.countReject(rejectGateCause)
	s.countReject(rejectFullCause)
	s.countReject(rejectFullCause)
	got := s.loadRejects()
	if got.Gate != 1 || got.Full != 2 {
		t.Fatalf("rejects = %+v, want gate 1 full 2", got)
	}
	if got.Cooldown != 0 || got.PerIP != 0 {
		t.Fatalf("rejects = %+v, want cooldown and perIP zero", got)
	}
}

// TestAdmissionRefusalsCount pins the call sites: a banned dial and a
// full IP slot each tally exactly once under their own cause.
func TestAdmissionRefusalsCount(t *testing.T) {
	s := pairServer(t)
	dyn := Cfg.Dynamic.Load()
	dyn.ConnectionCooldown = 0

	s.AuthFailsMu.Lock()
	s.AuthFails["192.0.2.6"] = RateLimitRecord{UnlockTime: time.Now().Add(time.Minute)}
	s.AuthFailsMu.Unlock()
	if s.CheckConnectionRate(httptest.NewRecorder(), "192.0.2.6") {
		t.Fatal("a banned address was allowed in")
	}

	s.IpCounts["192.0.2.7"] = dyn.MaxConnectionsPerIP
	if s.acquireIPConnection(httptest.NewRecorder(), "192.0.2.7") {
		t.Fatal("a full IP slot was allowed in")
	}

	got := s.loadRejects()
	if got.Cooldown != 1 || got.PerIP != 1 {
		t.Fatalf("rejects = %+v, want cooldown 1 perIP 1", got)
	}
}

// TestHubDropsCounted pins the shed counter: a frame for a wedged peer
// (full queue, no reader) tallies one drop instead of blocking.
func TestHubDropsCounted(t *testing.T) {
	s := NewChatServer()
	conn := &websocket.Conn{}
	full := make(chan []byte, 1)
	full <- []byte("wedged")
	s.Hub.Clients[conn] = &ClientSession{Conn: conn, Send: full}
	s.Hub.sendWithRetry(conn, s.Hub.Clients[conn], []byte("new"), false)
	if got := atomic.LoadInt64(&s.Hub.drops); got != 1 {
		t.Fatalf("drops = %d, want 1", got)
	}
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

package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/CleveTok3125/V2V/internal/behavior"
	"github.com/CleveTok3125/V2V/internal/pow"
	"github.com/CleveTok3125/V2V/internal/serverconfig"
)

func abuseForGateHTTP() *serverconfig.AbuseConfig {
	return &serverconfig.AbuseConfig{
		PowFirstConnect: "always",
		PowGateTier:     1,
		PowTierCount:    2,
		PowTiers: []serverconfig.PowTier{
			{},
			{T: 1, M: 8 * 1024, P: 1, Difficulty: 8, EstMs: 100},
		},
		PowTTL:        time.Minute,
		JoinWaitMin:   0,
		JoinWaitMax:   0,
		PassTTL:       time.Minute,
		PassSingleUse: true,
	}
}

func withGateAbuse(t *testing.T, cfg *serverconfig.AbuseConfig) {
	t.Helper()
	old := Cfg.Abuse.Load()
	Cfg.Abuse.Store(cfg)
	t.Cleanup(func() { Cfg.Abuse.Store(old) })
}

func postGate(t *testing.T, s *ChatServer, body string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/join-gate", bytes.NewBufferString(body))
	req.RemoteAddr = "198.51.100.7:4321"
	rec := httptest.NewRecorder()
	s.handleJoinGate(rec, req)
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("gate must answer JSON: %v", err)
	}
	return rec.Code, out
}

func TestHandleJoinGateRoundTrip(t *testing.T) {
	withGateAbuse(t, abuseForGateHTTP())
	s := NewChatServer()
	code, chal := postGate(t, s, `{"step":"challenge","ui":"cli"}`)
	if code != http.StatusOK {
		t.Fatalf("challenge status = %d (%v)", code, chal)
	}
	preset := pow.Preset{
		Time:       int(chal["pow"].(map[string]any)["t"].(float64)),
		Memory:     int(chal["pow"].(map[string]any)["m"].(float64)),
		Threads:    int(chal["pow"].(map[string]any)["p"].(float64)),
		Difficulty: uint(chal["pow"].(map[string]any)["difficulty"].(float64)),
	}
	nonce, err := pow.Solve(preset, chal["salt"].(string), nil)
	if err != nil {
		t.Fatal(err)
	}
	sub, _ := json.Marshal(map[string]any{
		"step": "submit", "challenge_id": chal["challenge_id"],
		"nonce": nonce, "ticket": chal["ticket"],
	})
	code, resp := postGate(t, s, string(sub))
	if code != http.StatusOK {
		t.Fatalf("submit status = %d (%v)", code, resp)
	}
	pass, _ := resp["pass"].(string)
	if pass == "" {
		t.Fatal("submit must return a pass")
	}
	// checkGatePass consumes the single-use pass.
	hsReq := httptest.NewRequest(http.MethodGet, "/?gate_pass="+pass, nil)
	hsReq.RemoteAddr = "198.51.100.7:4321"
	hsRec := httptest.NewRecorder()
	if !s.checkGatePass(hsRec, hsReq, "198.51.100.7") {
		t.Fatal("fresh pass must open the gate")
	}
	if s.checkGatePass(httptest.NewRecorder(), hsReq, "198.51.100.7") {
		t.Fatal("single-use pass must not open twice")
	}
}

func TestGateRequiredLogic(t *testing.T) {
	s := NewChatServer()
	withGateAbuse(t, abuseForGateHTTP())
	if !s.gateRequired() {
		t.Fatal("always mode must require the gate")
	}
	withGateAbuse(t, nil)
	if s.gateRequired() {
		t.Fatal("nil abuse must disable the gate")
	}
}

func TestGateTierBump(t *testing.T) {
	withGateAbuse(t, abuseForGateHTTP())
	s := NewChatServer()
	if got := s.gateTier(); got != 1 {
		t.Fatalf("tier = %d, want 1", got)
	}
}

func TestHTTPGateTierMode(t *testing.T) {
	cfg := abuseForGateHTTP()
	cfg.GateHTTPEnabled = true
	cfg.GateHTTPClasses = []string{"trip_verify"}
	cfg.GateHTTPMode = "tier"
	cfg.GateHTTPTierMin = 1
	withGateAbuse(t, cfg)
	s := NewChatServer()
	s.Behavior = NewBehaviorEngine(behavior.NewStore(100), behavior.NewStats(), stubGeo{}, "")

	if s.httpGateFor("trip_verify", "10.0.0.1") {
		t.Fatal("unscored HTTP-only IP must not gate")
	}
	now := time.Now()
	s.Behavior.ObserveConnect("10.0.0.2", "bot", behavior.IdGuest, now.Add(-time.Hour))
	for i := 0; i < 60; i++ {
		s.Behavior.ObserveMessage("10.0.0.2", now.Add(-time.Duration(60-i)*10*time.Second))
	}
	if tier, _ := s.Behavior.Score("10.0.0.2", testBehaviorConfig(), now, time.UTC); tier < 1 {
		t.Fatalf("fixture must produce a flagged tier, got %d", tier)
	}
	if !s.httpGateFor("trip_verify", "10.0.0.2") {
		t.Fatal("flagged IP must gate in tier mode")
	}
	if s.httpGateFor("meta", "10.0.0.2") {
		t.Fatal("unlisted class must never gate")
	}
}

func TestGatePassSingleUseConsumedOnce(t *testing.T) {
	cfg := abuseForGateHTTP()
	cfg.PassSingleUse = true
	withGateAbuse(t, cfg)
	s := NewChatServer()
	s.Gate.passes["tok"] = &GatePass{Token: "tok", IP: "10.0.0.9", Expires: time.Now().Add(time.Minute)}
	req := httptest.NewRequest(http.MethodGet, "/?gate_pass=tok", nil)
	req.RemoteAddr = "10.0.0.9:1234"
	if !s.checkGatePass(httptest.NewRecorder(), req, "10.0.0.9") {
		t.Fatal("fresh single-use pass must open once")
	}
	if s.checkGatePass(httptest.NewRecorder(), req, "10.0.0.9") {
		t.Fatal("single-use pass must be spent (ServeWS must call it exactly once)")
	}
}

func forceUnderAttack(t *testing.T, s *ChatServer) {
	t.Helper()
	p := SampleParams{EnterSecs: 1, EnterRPS: 1000, TTL: time.Minute, Force: "auto", ScaleMode: "max", ScaleW: [4]float64{1, 1, 1, 1}}
	now := time.Now()
	if !s.Attack.sample(true, now, p).Under {
		t.Fatal("forced sample must enter under attack")
	}
}

func TestHTTPPassGate(t *testing.T) {
	cfg := abuseForGateHTTP()
	cfg.GateHTTPEnabled = true
	cfg.GateHTTPClasses = []string{"trip_verify"}
	cfg.GateHTTPMode = "under-attack"
	withGateAbuse(t, cfg)
	s := NewChatServer()

	req := func() (*httptest.ResponseRecorder, *http.Request) {
		r := httptest.NewRequest(http.MethodGet, "/api/trip/verify?pub=x", nil)
		r.RemoteAddr = "198.51.100.7:4321"
		return httptest.NewRecorder(), r
	}
	// Not under attack: open.
	w, r := req()
	if !s.checkHTTPPass(w, r, "trip_verify", "198.51.100.7") {
		t.Fatal("must pass when not under attack")
	}
	// Unlisted class stays open even under attack.
	forceUnderAttack(t, s)
	w, r = req()
	if !s.checkHTTPPass(w, r, "meta", "198.51.100.7") {
		t.Fatal("unlisted class must stay open")
	}
	// Listed class without pass: 429.
	w, r = req()
	if s.checkHTTPPass(w, r, "trip_verify", "198.51.100.7") {
		t.Fatal("must require a pass under attack")
	}
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", w.Code)
	}
	// With a valid lease pass: open.
	s.Gate.passes["tok1"] = &GatePass{Token: "tok1", IP: "198.51.100.7", Expires: time.Now().Add(time.Minute)}
	r2 := httptest.NewRequest(http.MethodGet, "/api/trip/verify?pub=x&pass=tok1", nil)
	r2.RemoteAddr = "198.51.100.7:4321"
	if !s.checkHTTPPass(httptest.NewRecorder(), r2, "trip_verify", "198.51.100.7") {
		t.Fatal("valid lease pass must open")
	}
	// Gate disabled: open without pass.
	cfg.GateHTTPEnabled = false
	w, r = req()
	if !s.checkHTTPPass(w, r, "trip_verify", "198.51.100.7") {
		t.Fatal("disabled gate must stay open")
	}
}

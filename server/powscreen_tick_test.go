package main

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/CleveTok3125/V2V/internal/behavior"
	"github.com/CleveTok3125/V2V/internal/pow"
	"github.com/CleveTok3125/V2V/internal/serverconfig"
	"github.com/CleveTok3125/V2V/internal/wire"
)

func tickAbuse() *serverconfig.AbuseConfig {
	bc := testBehaviorConfig()
	bc.TierEnter = []float64{0, 0.25}
	bc.TierExit = []float64{0, 0.18}
	bc.RecheckMin = time.Second
	bc.RecheckMax = time.Second
	bc.Retention = []time.Duration{time.Hour, time.Hour}
	bc.BumpMax = 1
	bc.StatsEnabled = false
	return &serverconfig.AbuseConfig{
		PowTierCount:   2,
		PowTiers:       []serverconfig.PowTier{{}, {T: 1, M: 8 * 1024, P: 1, Difficulty: 8, EstMs: 100}},
		ScreenDeadline: time.Minute,
		Behavior:       bc,
	}
}

func tickServer() *ChatServer {
	s := NewChatServer()
	s.Behavior = NewBehaviorEngine(behavior.NewStore(100), behavior.NewStats(), stubGeo{}, "")
	s.Screener = NewPowScreener(s.Behavior)
	return s
}

func drainSend(sess *ClientSession) []string {
	var out []string
	for {
		select {
		case b := <-sess.Send:
			out = append(out, string(b))
		default:
			return out
		}
	}
}

func TestBehaviorTickChallengesBot(t *testing.T) {
	withGateAbuse(t, tickAbuse())
	s := tickServer()
	now := time.Now()
	ip := "10.0.0.20"
	conn := &websocket.Conn{}
	sess := &ClientSession{Conn: conn, IP: ip, Platform: "native", DisplayName: "bot", Send: make(chan []byte, 8)}
	s.Hub.Clients[conn] = sess
	s.Behavior.ObserveConnect(ip, "bot", behavior.IdGuest, now.Add(-time.Hour))
	for i := 0; i < 60; i++ {
		s.Behavior.ObserveMessage(ip, now.Add(-time.Duration(60-i)*10*time.Second))
	}
	var ls, lst time.Time = now, now
	s.behaviorTick(now, &ls, &lst)
	if !s.Screener.Has(conn) {
		t.Fatal("bot session must be challenged")
	}
	msgs := drainSend(sess)
	if len(msgs) < 2 {
		t.Fatalf("session must get notice + offer, got %d", len(msgs))
	}
	var offer wire.PowOffer
	found := false
	for _, m := range msgs {
		var probe struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal([]byte(m), &probe); err == nil && probe.Type == "pow_offer" {
			if err := json.Unmarshal([]byte(m), &offer); err != nil {
				t.Fatal(err)
			}
			found = true
		}
	}
	if !found || offer.Tier != 1 {
		t.Fatalf("offer missing or wrong tier: %+v", offer)
	}
	// Solve through the real frame path.
	ch, ok := s.Screener.Get(conn)
	if !ok {
		t.Fatal("challenge must be stored")
	}
	nonce, err := pow.Solve(ch.Preset, ch.Salt, nil)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(wire.PowResult{Type: "pow_result", ChallengeID: ch.ID, Nonce: nonce})
	s.handlePowFrame(sess, string(raw))
	if s.Screener.Has(conn) {
		t.Fatal("passed challenge must clear")
	}
	msgs = drainSend(sess)
	if len(msgs) == 0 {
		t.Fatal("ack expected after passing")
	}
}

func TestBehaviorTickSkipsClean(t *testing.T) {
	withGateAbuse(t, tickAbuse())
	s := tickServer()
	now := time.Now()
	conn := &websocket.Conn{}
	sess := &ClientSession{Conn: conn, IP: "10.0.0.21", Platform: "native", DisplayName: "quiet", Send: make(chan []byte, 8)}
	s.Hub.Clients[conn] = sess
	s.Behavior.ObserveConnect("10.0.0.21", "quiet", behavior.IdKey, now)
	var ls, lst time.Time = now, now
	s.behaviorTick(now, &ls, &lst)
	if s.Screener.Has(conn) {
		t.Fatal("clean key session must not be challenged")
	}
	if msgs := drainSend(sess); len(msgs) != 0 {
		t.Fatalf("clean session must get nothing, got %v", msgs)
	}
}

func TestHandlePowDeclineMutes(t *testing.T) {
	withGateAbuse(t, tickAbuse())
	s := tickServer()
	conn := &websocket.Conn{}
	sess := &ClientSession{Conn: conn, IP: "10.0.0.22", Send: make(chan []byte, 8)}
	s.Screener.Issue(conn, "10.0.0.22", 1, screenPreset, "salt-d", "ch-d", time.Now().Add(time.Minute))
	raw, _ := json.Marshal(wire.PowDecline{Type: "pow_decline", ChallengeID: "ch-d"})
	s.handlePowFrame(sess, string(raw))
	if !s.Screener.IsMuted(conn) {
		t.Fatal("declined session must be muted")
	}
}

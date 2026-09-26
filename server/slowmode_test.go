package main

import (
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/CleveTok3125/V2V/internal/behavior"
	"github.com/CleveTok3125/V2V/internal/guard"
	"github.com/CleveTok3125/V2V/internal/serverconfig"
	"github.com/CleveTok3125/V2V/internal/wire"
)

func slowServer(t *testing.T) *ChatServer {
	t.Helper()
	eng := NewBehaviorEngine(behavior.NewStore(100), behavior.NewStats(), stubGeo{}, "")
	s := &ChatServer{Behavior: eng, Screener: NewPowScreener(eng), SlowCooldown: guard.NewCooldownMap()}
	bc := testBehaviorConfig()
	bc.SlowmodeMult = []int{0, 0, 10, 60}
	old := Cfg.Abuse.Load()
	Cfg.Abuse.Store(&serverconfig.AbuseConfig{Behavior: bc})
	t.Cleanup(func() { Cfg.Abuse.Store(old) })
	return s
}

func slowSession(ip string) *ClientSession {
	return &ClientSession{
		Conn:        &websocket.Conn{},
		IP:          ip,
		DisplayName: "u",
		Send:        make(chan []byte, 8),
	}
}

func drainNotices(sess *ClientSession) []string {
	var out []string
	for {
		select {
		case m := <-sess.Send:
			out = append(out, string(m))
		default:
			return out
		}
	}
}

func TestAllowSlowSend(t *testing.T) {
	s := slowServer(t)
	sess := slowSession("10.9.9.10")
	base := time.Second
	// Tier 0: always allowed, never recorded.
	for i := 0; i < 3; i++ {
		if !s.allowSlowSend(sess, "10.9.9.10", base) {
			t.Fatal("tier 0 must pass")
		}
	}
	// Promote to tier 2: first passes and arms, immediate second drops
	// with a notice naming the effective 10s cooldown.
	s.Behavior.store.Get("10.9.9.10").Tier = 2
	if !s.allowSlowSend(sess, "10.9.9.10", base) {
		t.Fatal("first tier-2 send must pass")
	}
	if s.allowSlowSend(sess, "10.9.9.10", base) {
		t.Fatal("immediate second tier-2 send must drop")
	}
	var warned bool
	for _, m := range drainNotices(sess) {
		if strings.Contains(m, "chậm") && strings.Contains(m, "10s") {
			warned = true
		}
	}
	if !warned {
		t.Fatal("dropped send must warn with slowmode notice")
	}
	// Unlimited keeps the old bypass: never throttled, never warned.
	sess.Perms = wire.Permission{CanMessageUnlimited: true}
	for i := 0; i < 3; i++ {
		if !s.allowSlowSend(sess, "10.9.9.10", base) {
			t.Fatal("unlimited must keep existing bypass")
		}
	}
}

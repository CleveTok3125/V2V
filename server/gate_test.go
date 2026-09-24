package main

import (
	"testing"
	"time"

	"github.com/CleveTok3125/V2V/internal/pow"
)

var testGatePreset = pow.Preset{Time: 1, Memory: 8 * 1024, Threads: 1, Difficulty: 8}

func solveFor(t *testing.T, c *GateChallenge) uint64 {
	t.Helper()
	nonce, err := pow.Solve(pow.Preset{Time: c.Preset.Time, Memory: c.Preset.Memory, Threads: c.Preset.Threads, Difficulty: c.Preset.Difficulty}, c.Salt, nil)
	if err != nil {
		t.Fatal(err)
	}
	return nonce
}

func TestGateFullFlow(t *testing.T) {
	g := NewGateStore()
	c, err := g.Request("10.0.0.1", 1, testGatePreset, 0, 0, time.Minute, "serverpub")
	if err != nil {
		t.Fatal(err)
	}
	nonce := solveFor(t, c)
	pass, res := g.Submit("10.0.0.1", c.ID, nonce, c.Ticket, time.Minute)
	if res != SubmitOK || pass == nil {
		t.Fatalf("submit = %v, %v", pass, res)
	}
	if !g.UsePass("10.0.0.1", pass.Token, true) {
		t.Fatal("fresh pass must validate")
	}
	if g.UsePass("10.0.0.1", pass.Token, true) {
		t.Fatal("single-use pass must not validate twice")
	}
}

func TestGateEarly(t *testing.T) {
	g := NewGateStore()
	c, err := g.Request("10.0.0.2", 1, testGatePreset, time.Hour, 2*time.Hour, 3*time.Hour, "serverpub")
	if err != nil {
		t.Fatal(err)
	}
	_, res := g.Submit("10.0.0.2", c.ID, 0, c.Ticket, time.Minute)
	if res != SubmitEarly {
		t.Fatalf("early submit must be Early, got %v", res)
	}
}

func TestGateTamper(t *testing.T) {
	g := NewGateStore()
	// One challenge per tamper: a bad submit consumes its challenge.
	mk := func(ip string) *GateChallenge {
		t.Helper()
		c, err := g.Request(ip, 1, testGatePreset, 0, 0, time.Minute, "serverpub")
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	c1 := mk("10.0.1.1")
	nonce := solveFor(t, c1)
	if _, res := g.Submit("10.0.1.1", c1.ID, nonce, "bad-ticket", time.Minute); res != SubmitBad {
		t.Fatalf("bad ticket must be Bad, got %v", res)
	}
	c2 := mk("10.0.1.2")
	nonce2 := solveFor(t, c2)
	if _, res := g.Submit("10.9.9.9", c2.ID, nonce2, c2.Ticket, time.Minute); res != SubmitBad {
		t.Fatalf("wrong IP must be Bad, got %v", res)
	}
	c3 := mk("10.0.1.3")
	nonce3 := solveFor(t, c3)
	if _, res := g.Submit("10.0.1.3", c3.ID, nonce3+1, c3.Ticket, time.Minute); res != SubmitBad {
		t.Fatalf("wrong nonce must be Bad, got %v", res)
	}
	// Consumed by the failed verify: gone now.
	if _, res := g.Submit("10.0.1.3", c3.ID, nonce3, c3.Ticket, time.Minute); res != SubmitGone {
		t.Fatalf("consumed challenge must be Gone, got %v", res)
	}
}

func TestGateExpiry(t *testing.T) {
	g := NewGateStore()
	c, err := g.Request("10.0.0.4", 1, testGatePreset, 0, 0, time.Millisecond, "serverpub")
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(5 * time.Millisecond)
	if _, res := g.Submit("10.0.0.4", c.ID, 0, c.Ticket, time.Minute); res != SubmitGone {
		t.Fatalf("expired challenge must be Gone, got %v", res)
	}
}

func TestGatePassReusableAndExpiry(t *testing.T) {
	g := NewGateStore()
	c, err := g.Request("10.0.0.5", 1, testGatePreset, 0, 0, time.Minute, "serverpub")
	if err != nil {
		t.Fatal(err)
	}
	nonce := solveFor(t, c)
	pass, res := g.Submit("10.0.0.5", c.ID, nonce, c.Ticket, 20*time.Millisecond)
	if res != SubmitOK {
		t.Fatal(res)
	}
	if !g.UsePass("10.0.0.5", pass.Token, false) || !g.UsePass("10.0.0.5", pass.Token, false) {
		t.Fatal("reusable pass must validate repeatedly")
	}
	if g.UsePass("10.9.9.9", pass.Token, false) {
		t.Fatal("pass must bind IP")
	}
	time.Sleep(30 * time.Millisecond)
	if g.UsePass("10.0.0.5", pass.Token, false) {
		t.Fatal("expired pass must fail")
	}
}

func TestGateRequestCooldown(t *testing.T) {
	g := NewGateStore()
	if _, err := g.Request("10.0.0.6", 1, testGatePreset, 0, 0, time.Minute, "serverpub"); err != nil {
		t.Fatal(err)
	}
	if _, err := g.Request("10.0.0.6", 1, testGatePreset, 0, 0, time.Minute, "serverpub"); err == nil {
		t.Fatal("rapid re-request must be throttled")
	}
}

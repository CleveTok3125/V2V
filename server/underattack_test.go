package main

import (
	"testing"
	"time"
)

func testSampleParams() SampleParams {
	return SampleParams{
		EnterSecs: 2,
		EnterRPS:  10,
		TTL:       10 * time.Second,
		Force:     "auto",
		ScaleMode: "max",
		ScaleW:    [4]float64{1, 1, 1, 1},
	}
}

func TestUnderAttackEnterOnSustainedFull(t *testing.T) {
	a := NewAttackState()
	now := time.Now()
	p := testSampleParams()
	if a.sample(false, now, p).Under {
		t.Fatal("must not enter when not full")
	}
	if a.sample(true, now.Add(time.Second), p).Under {
		t.Fatal("must need sustained seconds")
	}
	if !a.sample(true, now.Add(2*time.Second), p).Under {
		t.Fatal("must enter after sustained full")
	}
}

func TestUnderAttackEnterOnRejectRate(t *testing.T) {
	a := NewAttackState()
	now := time.Now()
	p := testSampleParams()
	for i := 0; i < 60; i++ {
		a.noteAttempt("10.0.0.1")
		a.note503()
	}
	// 60 rejects in the 5s window with ~60 attempts: ratio 1, and the
	// RPS leg trips via the reject burst even without full slots.
	st := a.sample(false, now, p)
	_ = st
	st = a.sample(false, now.Add(time.Second), p)
	if !st.Under {
		t.Fatal("reject burst must enter after sustained seconds")
	}
}

func TestUnderAttackExitAfterQuiet(t *testing.T) {
	a := NewAttackState()
	now := time.Now()
	p := testSampleParams()
	a.sample(true, now, p)
	a.sample(true, now.Add(time.Second), p) // enter
	if !a.IsUnderAttack() {
		t.Fatal("must be under attack")
	}
	// Quiet for longer than the TTL: exit.
	if a.sample(false, now.Add(20*time.Second), p).Under {
		t.Fatal("must exit after quiet TTL")
	}
	if a.IsUnderAttack() {
		t.Fatal("flag must clear")
	}
}

func TestUnderAttackRearm(t *testing.T) {
	a := NewAttackState()
	now := time.Now()
	p := testSampleParams()
	a.sample(true, now, p)
	a.sample(true, now.Add(time.Second), p)         // enter, until=+10s
	st := a.sample(true, now.Add(9*time.Second), p) // still hot: re-arm
	if !st.Under {
		t.Fatal("must stay under while hot")
	}
	if a.sample(false, now.Add(25*time.Second), p).Under {
		t.Fatal("must exit once quiet past re-armed TTL")
	}
}

func TestUnderAttackForce(t *testing.T) {
	a := NewAttackState()
	now := time.Now()
	p := testSampleParams()
	p.Force = "on"
	if !a.sample(false, now, p).Under {
		t.Fatal("force=on must hold under attack")
	}
	p.Force = "off"
	if a.sample(true, now.Add(time.Second), p).Under {
		t.Fatal("force=off must suppress attack")
	}
}

func TestAttackScaleBounded(t *testing.T) {
	a := NewAttackState()
	now := time.Now()
	for i := 0; i < 100; i++ {
		a.noteAttempt("10.0.0.99")
		a.noteGate()
	}
	st := a.sample(false, now, testSampleParams())
	if st.Scale < 0 || st.Scale > 1 {
		t.Fatalf("scale must be in [0,1], got %v", st.Scale)
	}
	if st.Scale <= 0 {
		t.Fatal("heavy gate rejects must raise scale")
	}
}

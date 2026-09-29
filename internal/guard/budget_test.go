package guard

import (
	"testing"
	"time"
)

func TestBudgetMapBurstAndRefill(t *testing.T) {
	b := NewBudgetMap()
	now := time.Unix(0, 0)
	b.now = func() time.Time { return now }

	// Burst 100, rate 10/s: two 50-line requests pass, the third fails.
	if !b.Allow("ip", 50, 100, 10) {
		t.Fatal("first charge must pass")
	}
	if !b.Allow("ip", 50, 100, 10) {
		t.Fatal("second charge must pass")
	}
	if b.Allow("ip", 50, 100, 10) {
		t.Fatal("over-budget charge must be refused")
	}
	// After 1s the bucket refills 10 tokens: a 10-line charge passes.
	now = now.Add(time.Second)
	if !b.Allow("ip", 10, 100, 10) {
		t.Fatal("refilled charge must pass")
	}
	// Still short for 20.
	if b.Allow("ip", 20, 100, 10) {
		t.Fatal("charge above the refilled balance must be refused")
	}
}

func TestBudgetMapIsolationAndFree(t *testing.T) {
	b := NewBudgetMap()
	now := time.Unix(0, 0)
	b.now = func() time.Time { return now }

	if !b.Allow("ip", 0, 10, 1) {
		t.Fatal("zero cost must be free")
	}
	if !b.Allow("a", 10, 10, 1) {
		t.Fatal("a must spend its own bucket")
	}
	if !b.Allow("b", 10, 10, 1) {
		t.Fatal("b must not be affected by a")
	}
	// A non-positive burst refuses (fail closed).
	if b.Allow("c", 1, 0, 1) {
		t.Fatal("zero burst must refuse")
	}
}

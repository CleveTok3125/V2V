package main

import (
	"testing"

	"github.com/CleveTok3125/V2V/lib/pow"
)

func TestPlatformSolvePoW(t *testing.T) {
	p := pow.Preset{Time: 1, Memory: 8 * 1024, Threads: 1, Difficulty: 8}
	nonce, err := platformSolvePoW(p, "budget-salt", 60_000)
	if err != nil {
		t.Fatal(err)
	}
	if !pow.Verify(p, "budget-salt", nonce) {
		t.Fatal("solution must verify")
	}
}

func TestPlatformSolvePoWTimeout(t *testing.T) {
	p := pow.Preset{Time: 1, Memory: 8 * 1024, Threads: 1, Difficulty: 40}
	if _, err := platformSolvePoW(p, "budget-salt-2", 5); err == nil {
		t.Fatal("impossible solve must time out")
	}
}

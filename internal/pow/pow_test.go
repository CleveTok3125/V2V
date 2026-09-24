package pow

import (
	"crypto/sha256"
	"testing"
)

func TestLeadingZeros(t *testing.T) {
	cases := []struct {
		in   [32]byte
		want uint
	}{
		{sha256.Sum256([]byte("a")), 0}, // 0xca... starts with 1-bits
		{[32]byte{}, 256},
		{[32]byte{0x00, 0x80}, 8},
		{[32]byte{0x0f}, 4},
		{[32]byte{0x01}, 7},
		{[32]byte{0xff}, 0},
	}
	for i, c := range cases {
		if got := LeadingZeros(c.in); got != c.want {
			t.Fatalf("case %d: got %d want %d", i, got, c.want)
		}
	}
}

func tinyPreset() Preset {
	return Preset{Time: 1, Memory: 8 * 1024, Threads: 1, Difficulty: 10}
}

func TestSolveVerifyRoundTrip(t *testing.T) {
	p := tinyPreset()
	nonce, err := Solve(p, "test-salt-v1", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !Verify(p, "test-salt-v1", nonce) {
		t.Fatal("fresh solution must verify")
	}
}

func TestVerifyRejectsTamper(t *testing.T) {
	p := tinyPreset()
	nonce, err := Solve(p, "tamper-salt", nil)
	if err != nil {
		t.Fatal(err)
	}
	if Verify(p, "tamper-salt", nonce+1) {
		t.Fatal("wrong nonce must fail")
	}
	if Verify(p, "other-salt", nonce) {
		t.Fatal("wrong salt must fail")
	}
	harder := p
	harder.Difficulty = 64
	if Verify(harder, "tamper-salt", nonce) {
		t.Fatal("raised difficulty must fail the same nonce")
	}
}

func TestClampBounds(t *testing.T) {
	in := Preset{Time: 0, Memory: 1, Threads: 99, Difficulty: 999}
	got, changed := Clamp(in, false)
	if !changed {
		t.Fatal("out-of-range preset must be clamped")
	}
	if got.Time != MinTime || got.Memory != MinMemoryKiB || got.Threads != MaxThreads || got.Difficulty != MaxDifficulty {
		t.Fatalf("unexpected clamp result: %+v", got)
	}
	wasm := Preset{Time: 1, Memory: 8 * 1024, Threads: 4, Difficulty: 8}
	got, changed = Clamp(wasm, true)
	if !changed || got.Threads != 1 {
		t.Fatalf("wasm must force P=1: %+v", got)
	}
	good := Preset{Time: 1, Memory: 8 * 1024, Threads: 1, Difficulty: 8}
	if _, changed = Clamp(good, false); changed {
		t.Fatal("in-range preset must pass through unchanged")
	}
}

func TestSolveAbort(t *testing.T) {
	p := Preset{Time: 1, Memory: 8 * 1024, Threads: 1, Difficulty: 64}
	stop := make(chan struct{})
	close(stop)
	if _, err := Solve(p, "abort-salt", stop); err != ErrAborted {
		t.Fatalf("closed stopCh must abort, got %v", err)
	}
}

func TestZeroDifficulty(t *testing.T) {
	p := Preset{Time: 1, Memory: 8 * 1024, Threads: 1, Difficulty: 0}
	nonce, err := Solve(p, "zero-diff", nil)
	if err != nil {
		t.Fatal(err)
	}
	if nonce != 0 {
		t.Fatalf("difficulty 0 must accept nonce 0, got %d", nonce)
	}
	if !Verify(p, "zero-diff", 0) {
		t.Fatal("difficulty 0 must verify nonce 0")
	}
}

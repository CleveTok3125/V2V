//go:build !js

package main

import (
	"errors"
	"time"

	"golang.org/x/crypto/argon2"

	"github.com/CleveTok3125/V2V/internal/pow"
)

// platformArgon2 runs argon2id in-process on the desktop/native build.
func platformArgon2(pass, salt []byte, t, m uint32, threads uint8) []byte {
	return argon2.IDKey(pass, salt, t, m, threads, 32)
}

// platformSolvePoW solves in a goroutine with a wall-clock budget; a
// native build has real OS threads, so the UI is never blocked.
func platformSolvePoW(p pow.Preset, salt string, maxCostMs int64) (uint64, error) {
	stop := make(chan struct{})
	defer close(stop)
	type result struct {
		nonce uint64
		err   error
	}
	done := make(chan result, 1)
	go func() {
		n, err := pow.Solve(p, salt, stop)
		done <- result{n, err}
	}()
	if maxCostMs <= 0 {
		r := <-done
		return r.nonce, r.err
	}
	timer := time.NewTimer(time.Duration(maxCostMs) * time.Millisecond)
	defer timer.Stop()
	select {
	case r := <-done:
		return r.nonce, r.err
	case <-timer.C:
		return 0, errors.New("pow budget exceeded, declining")
	}
}

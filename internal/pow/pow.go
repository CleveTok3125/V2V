// Package pow is argon2id-backed proof of work with a fine-grained
// sha256 difficulty search. Each challenge runs one argon2id (memory
// hard, ASIC-resistant) and then searches a nonce whose
// sha256(key||nonce) carries Difficulty leading zero bits. The server
// verifies cheaply with one argon2id plus one hash; difficulty tunes
// solving cost without touching the memory-hard part.
//
// Preset bounds mirror internal/identity argon2 clamps (Time 1-10,
// Memory 8MiB-256MiB in KiB, Threads 1-8); wasm forces Threads=1. The
// concrete ladder lives in config, never here.
package pow

import (
	"crypto/sha256"
	"encoding/binary"
	"errors"

	"golang.org/x/crypto/argon2"
)

// Domain separates PoW key derivation from tripcode/file KDFs.
const Domain = "V2V-pow-v1"

const (
	MinTime      = 1
	MaxTime      = 10
	MinMemoryKiB = 8 * 1024
	MaxMemoryKiB = 256 * 1024
	MinThreads   = 1
	MaxThreads   = 8
	// MaxDifficulty caps the sha256 search at 64 leading zero bits;
	// beyond that solving time stops being plannable.
	MaxDifficulty = 64
)

// Preset is one PoW tier: argon2id cost plus the sha256 search width.
// Memory is KiB, matching x/crypto and the tripcode presets.
type Preset struct {
	Time       int
	Memory     int
	Threads    int
	Difficulty uint
}

// ErrAborted reports a Solve stopped through stopCh before finding a nonce.
var ErrAborted = errors.New("pow solve aborted")

// Clamp bounds a preset to the safe envelope (wasm forces Threads=1).
// It reports whether anything changed.
func Clamp(p Preset, wasm bool) (Preset, bool) {
	out := p
	changed := false
	set := func(v, lo, hi int) int {
		if v < lo {
			changed = true
			return lo
		}
		if v > hi {
			changed = true
			return hi
		}
		return v
	}
	out.Time = set(p.Time, MinTime, MaxTime)
	out.Memory = set(p.Memory, MinMemoryKiB, MaxMemoryKiB)
	out.Threads = set(p.Threads, MinThreads, MaxThreads)
	if wasm && out.Threads != 1 {
		out.Threads = 1
		changed = true
	}
	if p.Difficulty > MaxDifficulty {
		out.Difficulty = MaxDifficulty
		changed = true
	}
	return out, changed
}

// DeriveKey runs the single memory-hard step for a challenge salt.
// The salt must already bind challenge, server and session identity.
func DeriveKey(p Preset, salt string) []byte {
	return argon2.IDKey([]byte(Domain), []byte(salt), uint32(p.Time), uint32(p.Memory), uint8(p.Threads), 32)
}

// LeadingZeros counts leading zero bits of a 256-bit digest.
func LeadingZeros(h [32]byte) uint {
	var n uint
	for _, b := range h {
		if b == 0 {
			n += 8
			continue
		}
		for i := 7; i >= 0; i-- {
			if b&(1<<uint(i)) != 0 {
				return n
			}
			n++
		}
		return n
	}
	return n
}

func candidate(key []byte, nonce uint64) [32]byte {
	var nb [8]byte
	binary.LittleEndian.PutUint64(nb[:], nonce)
	buf := make([]byte, 0, len(key)+8)
	buf = append(buf, key...)
	buf = append(buf, nb[:]...)
	return sha256.Sum256(buf)
}

// Solve finds the smallest nonce meeting p.Difficulty for salt. It
// stops early when stopCh closes (nil channel means run to completion).
func Solve(p Preset, salt string, stopCh <-chan struct{}) (uint64, error) {
	key := DeriveKey(p, salt)
	for nonce := uint64(0); ; nonce++ {
		select {
		case <-stopCh:
			return 0, ErrAborted
		default:
		}
		if LeadingZeros(candidate(key, nonce)) >= p.Difficulty {
			return nonce, nil
		}
	}
}

// Verify recomputes one argon2id plus one hash: cheap by construction.
func Verify(p Preset, salt string, nonce uint64) bool {
	key := DeriveKey(p, salt)
	return LeadingZeros(candidate(key, nonce)) >= p.Difficulty
}

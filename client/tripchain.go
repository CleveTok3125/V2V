package main

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"runtime"

	"golang.org/x/crypto/argon2"
	"localchat/identity"
)

type TripState struct {
	Priv ed25519.PrivateKey
	Pub  ed25519.PublicKey
	Seq  uint32
	Prev []byte // 32 bytes
	Badge string
}

// deriveTripKey derives an ed25519 keypair from passphrase + serverPub hex.
// Salt is sha256(serverPubHex)[:16] to bind per-server. If serverPub empty, uses fixed salt.
func deriveTripKey(passphrase string, serverPubHex string) (ed25519.PrivateKey, ed25519.PublicKey, string) {
	var salt []byte
	if serverPubHex != "" {
		if b, err := hex.DecodeString(serverPubHex); err == nil && len(b) > 0 {
			h := sha256.Sum256(b)
			salt = h[:16]
		}
	}
	if salt == nil {
		h := sha256.Sum256([]byte("V2V-trip-v1"))
		salt = h[:16]
	}
	p := identity.PresetNative
	// Use WASM preset when running under js/wasm to avoid 64MB allocation which may OOM
	if isWASMRuntime() {
		p = identity.PresetWASM
	}
	key := argon2.IDKey([]byte(passphrase), salt, p.Time, p.Memory, p.Threads, 32)
	defer zeroTripBytes(key)
	priv := ed25519.NewKeyFromSeed(key)
	pub := priv.Public().(ed25519.PublicKey)
	h := sha256.Sum256(pub)
	badge := hex.EncodeToString(h[:])[:8]
	return priv, pub, badge
}

func deriveTripKeyWASM(passphrase string, serverPubHex string) (ed25519.PrivateKey, ed25519.PublicKey, string) {
	// Alias for explicit WASM call
	return deriveTripKey(passphrase, serverPubHex)
}

func isWASMRuntime() bool {
	return runtime.GOOS == "js"
}

func canonicalPayload(serverPub string, seq uint32, prev []byte, msgHash []byte, pub []byte) []byte {
	return []byte(fmt.Sprintf("%s\x00%d\x00%x\x00%x\x00%x", serverPub, seq, prev, msgHash, pub))
}

func zeroTripBytes(b []byte) {
	for i := range b {
		b[i] = 0
	}
}

// TripMessage is the JSON envelope for signed chat messages.
type TripMessage struct {
	Text string `json:"text,omitempty"` // for compatibility, also support Msg alias
	Msg  string `json:"msg,omitempty"`
	Pub  string `json:"pub,omitempty"`
	Seq  uint32 `json:"seq,omitempty"`
	Prev string `json:"prev,omitempty"` // hex 64
	Sig  string `json:"sig,omitempty"`  // hex 128
}

func (m *TripMessage) GetText() string {
	if m.Text != "" {
		return m.Text
	}
	return m.Msg
}

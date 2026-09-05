package main

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"runtime"

	"golang.org/x/crypto/argon2"
	"github.com/CleveTok3125/V2V/identity"
	"github.com/CleveTok3125/V2V/internal/tripcolor"
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

func canonicalPayload(serverPub string, seq uint32, prev []byte, msgHash []byte, pub []byte, displayName string, tmpID uint64) []byte {
	return tripcolor.CanonicalPayload(serverPub, seq, prev, msgHash, pub, displayName, tmpID)
}

func badgeColor(badge string) string {
	if ClientCfg != nil && len(ClientCfg.UI.TripPalette) > 0 {
		return tripcolor.BadgeColorWithPalette(badge, ClientCfg.UI.TripPalette)
	}
	return tripcolor.BadgeColor(badge)
}

func zeroTripBytes(b []byte) {
	for i := range b {
		b[i] = 0
	}
}

// TripMessage is the JSON envelope for signed chat messages.
// TmpID is the sender's per-session counter, bound into the signature so
// the server cannot renumber messages without breaking verification.
type TripMessage struct {
	Text        string `json:"text,omitempty"` // for compatibility, also support Msg alias
	Msg         string `json:"msg,omitempty"`
	Pub         string `json:"pub,omitempty"`
	Seq         uint32 `json:"seq,omitempty"`
	Prev        string `json:"prev,omitempty"` // hex 64
	Sig         string `json:"sig,omitempty"`  // hex 128
	DisplayName string `json:"display_name,omitempty"`
	TmpID       uint64 `json:"tmp_id,omitempty"`
}

// PlainMessage is the JSON envelope for unsigned chat messages. Break:
// raw text is no longer accepted; every message carries a per-session
// counter the server relays verbatim but never assigns.
type PlainMessage struct {
	TmpID uint64 `json:"tmp_id"`
	Text  string `json:"text"`
}

func (m *TripMessage) GetText() string {
	if m.Text != "" {
		return m.Text
	}
	return m.Msg
}

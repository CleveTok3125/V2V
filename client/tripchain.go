package main

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"runtime"
	"strings"

	"github.com/CleveTok3125/V2V/lib/identity"
	"github.com/CleveTok3125/V2V/lib/tripcolor"
)

// deriveTripKey derives an ed25519 keypair from a passphrase and a
// resolved 16-byte salt. The badge is a continuity aid, not an identity
// proof. Revealing the 32-byte seed exposes the identity (see lib/trip
// VerifySeed).
func deriveTripKey(passphrase string, salt []byte) (ed25519.PrivateKey, ed25519.PublicKey, string) {
	p := identity.PresetNative
	// Use WASM preset when running under js/wasm to avoid 64MB allocation which may OOM
	if isWASMRuntime() {
		p = identity.PresetWASM
	}
	key := platformArgon2([]byte(passphrase), salt, p.Time, p.Memory, p.Threads)
	if key == nil {
		return nil, nil, ""
	}
	defer identity.ZeroBytes(key)
	priv := ed25519.NewKeyFromSeed(key)
	pub := priv.Public().(ed25519.PublicKey)
	h := sha256.Sum256(pub)
	badge := hex.EncodeToString(h[:])[:8]
	return priv, pub, badge
}

func isWASMRuntime() bool {
	return runtime.GOOS == "js"
}

// tripServerDefault is the 16-byte server salt for one challenge: the
// advertised TRIP_SALT domain when set, else the server identity, else
// the fixed fallback for servers without identity.
func tripServerDefault(ch AuthPacket) []byte {
	if s := strings.TrimSpace(ch.TripSalt); s != "" {
		h := sha256.Sum256([]byte(s))
		return h[:16]
	}
	if b, err := hex.DecodeString(strings.TrimSpace(ch.ServerPubKey)); err == nil && len(b) > 0 {
		h := sha256.Sum256(b)
		return h[:16]
	}
	h := sha256.Sum256([]byte(tripSaltDomain()))
	return h[:16]
}

// resolveTripSalt maps the salt choice to 16 argon2 bytes: custom value
// when set (portable identity), else the server default.
func resolveTripSalt(customSalt string, serverDefault []byte) []byte {
	if customSalt != "" {
		h := sha256.Sum256([]byte(customSalt))
		return h[:16]
	}
	return serverDefault
}

// tripSaltDomain is the fixed argon2 domain for the no-identity fallback.
func tripSaltDomain() string {
	if ClientCfg != nil && ClientCfg.Crypto.SaltFallback != "" {
		return ClientCfg.Crypto.SaltFallback
	}
	return "V2V-trip-v1"
}

// formatSaltSource returns a localized label for the active salt source.
func formatSaltSource(src string, ch AuthPacket) string {
	switch src {
	case "env V2V_TRIP_SALT":
		return "biến môi trường V2V_TRIP_SALT"
	case "tripcode.json":
		return "file tripcode.json"
	case "config crypto.salt":
		if isWASMRuntime() {
			return "salt riêng"
		}
		return "cấu hình crypto.salt"
	default:
		if strings.TrimSpace(ch.TripSalt) != "" {
			return "mặc định theo server (TRIP_SALT)"
		}
		return "mặc định theo server"
	}
}

func badgeRGB(badge string) (r, g, b uint8) {
	pal := [][3]int(nil)
	if ClientCfg != nil {
		pal = ClientCfg.UI.TripPalette
	}
	return tripcolor.BadgeRGBWithPalette(badge, pal)
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
	// ReplyTo quotes a chain height for replies, bound into the trip
	// signature and covered by the chain link (v2+).
	ReplyTo uint64 `json:"reply_to,omitempty"`
}

// PlainMessage is the JSON envelope for unsigned chat messages. Break:
// raw text is no longer accepted; every message carries a per-session
// counter the server relays verbatim but never assigns.
type PlainMessage struct {
	TmpID   uint64 `json:"tmp_id"`
	Text    string `json:"text"`
	ReplyTo uint64 `json:"reply_to,omitempty"`
}

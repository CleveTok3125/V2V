package trip

import (
	"crypto/ed25519"
	"encoding/hex"
	"strings"
)

// PubFromSeed derives the tripcode pubkey from a 32-byte seed.
// A revealed seed lets anyone sign as that pubkey, so continuity ends.
// No revocation list is involved: imitability is the invalidation.
func PubFromSeed(seed []byte) (string, bool) {
	if len(seed) != ed25519.SeedSize {
		return "", false
	}
	priv := ed25519.NewKeyFromSeed(seed)
	pub := priv.Public().(ed25519.PublicKey)
	return hex.EncodeToString(pub), true
}

// VerifySeed reports whether seedHex derives to pubHex.
func VerifySeed(seedHex, pubHex string) bool {
	seedHex = strings.ToLower(strings.TrimSpace(seedHex))
	pubHex = strings.ToLower(strings.TrimSpace(pubHex))
	if len(seedHex) != ed25519.SeedSize*2 || len(pubHex) != ed25519.PublicKeySize*2 {
		return false
	}
	seed, err := hex.DecodeString(seedHex)
	if err != nil {
		return false
	}
	got, ok := PubFromSeed(seed)
	if !ok {
		return false
	}
	return got == pubHex
}

// revealDomain separates a reveal signature from any other signature made
// by the same key, so a message signature cannot pass as a reveal.
const revealDomain = "v2v.trip.reveal.v1"

// RevealPayload returns the bytes a reveal signature covers.
func RevealPayload(pubHex string) []byte {
	return []byte(revealDomain + "\x00" + strings.ToLower(strings.TrimSpace(pubHex)))
}

// SignReveal signs the pub the reveal exposes, so the stored announcement
// proves a key holder authored it.
func SignReveal(priv ed25519.PrivateKey, pubHex string) string {
	if len(priv) != ed25519.PrivateKeySize {
		return ""
	}
	return hex.EncodeToString(ed25519.Sign(priv, RevealPayload(pubHex)))
}

// VerifyReveal reports whether sigHex is a reveal signature for pubHex.
func VerifyReveal(pubHex, sigHex string) bool {
	pub, err := hex.DecodeString(strings.TrimSpace(pubHex))
	if err != nil || len(pub) != ed25519.PublicKeySize {
		return false
	}
	sig, err := hex.DecodeString(strings.TrimSpace(sigHex))
	if err != nil || len(sig) != ed25519.SignatureSize {
		return false
	}
	return ed25519.Verify(pub, RevealPayload(pubHex), sig)
}

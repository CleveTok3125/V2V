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

// IsSeedReveal reports whether text is exactly the seed for pubHex: a
// whole message containing only its own seed. Only a key holder can
// produce it, so it proves ownership at reveal time.
func IsSeedReveal(text, pubHex string) bool {
	seedHex := strings.TrimSpace(text)
	if len(seedHex) != ed25519.SeedSize*2 {
		return false
	}
	return VerifySeed(seedHex, pubHex)
}

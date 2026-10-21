package trip

import (
	"crypto/ed25519"
	"encoding/hex"
	"strings"
	"testing"
)

func TestVerifySeedRoundtrip(t *testing.T) {
	seed := make([]byte, 32)
	for i := range seed {
		seed[i] = byte(i + 9)
	}
	priv := ed25519.NewKeyFromSeed(seed)
	pubHex := hex.EncodeToString(priv.Public().(ed25519.PublicKey))
	if !VerifySeed(hex.EncodeToString(seed), pubHex) {
		t.Fatal("matching seed and pub must verify")
	}
}

func TestVerifySeedRejects(t *testing.T) {
	seed := make([]byte, 32)
	for i := range seed {
		seed[i] = byte(i + 9)
	}
	priv := ed25519.NewKeyFromSeed(seed)
	pubHex := hex.EncodeToString(priv.Public().(ed25519.PublicKey))
	other := make([]byte, 32)
	for i := range other {
		other[i] = byte(255 - i)
	}
	if VerifySeed(hex.EncodeToString(other), pubHex) {
		t.Fatal("wrong seed must not verify")
	}
	if VerifySeed(hex.EncodeToString(seed), hex.EncodeToString(other)) {
		t.Fatal("wrong pub must not verify")
	}
	for _, bad := range []struct{ seed, pub string }{
		{"zz", pubHex},
		{hex.EncodeToString(seed), "zz"},
		{"", ""},
		{hex.EncodeToString(seed)[:60], pubHex},
	} {
		if VerifySeed(bad.seed, bad.pub) {
			t.Fatalf("malformed input must not verify: %+v", bad)
		}
	}
}

func TestRevealSignature(t *testing.T) {
	seed := make([]byte, ed25519.SeedSize)
	for i := range seed {
		seed[i] = byte(i + 3)
	}
	priv := ed25519.NewKeyFromSeed(seed)
	pubHex := hex.EncodeToString(priv.Public().(ed25519.PublicKey))
	sig := SignReveal(priv, pubHex)
	if !VerifyReveal(pubHex, sig) {
		t.Fatal("own reveal signature must verify")
	}
	// Uppercase hex is the same pub.
	if !VerifyReveal(strings.ToUpper(pubHex), sig) {
		t.Fatal("uppercase pub must verify")
	}
	other := make([]byte, ed25519.SeedSize)
	for i := range other {
		other[i] = byte(200 - i)
	}
	otherPub := hex.EncodeToString(ed25519.NewKeyFromSeed(other).Public().(ed25519.PublicKey))
	if VerifyReveal(otherPub, sig) {
		t.Fatal("a signature over one pub must not verify for another")
	}
	if VerifyReveal(pubHex, "") || VerifyReveal(pubHex, "zz") || VerifyReveal("zz", sig) {
		t.Fatal("malformed input must not verify")
	}
	// A message signature is not a reveal signature.
	if VerifyReveal(pubHex, hex.EncodeToString(ed25519.Sign(priv, []byte(pubHex)))) {
		t.Fatal("message signature must not pass as a reveal")
	}
}

package trip

import (
	"crypto/ed25519"
	"encoding/hex"
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

func TestIsSeedReveal(t *testing.T) {
	seed := make([]byte, 32)
	for i := range seed {
		seed[i] = byte(i + 11)
	}
	priv := ed25519.NewKeyFromSeed(seed)
	pubHex := hex.EncodeToString(priv.Public().(ed25519.PublicKey))
	seedHex := hex.EncodeToString(seed)
	if !IsSeedReveal(seedHex, pubHex) {
		t.Fatal("exact seed text must announce")
	}
	if !IsSeedReveal("  "+seedHex+"\n", pubHex) {
		t.Fatal("surrounding whitespace must announce")
	}
	for _, text := range []string{
		"",
		"hello",
		"seed: " + seedHex,
		seedHex + " extra",
		seedHex[:60],
		hex.EncodeToString(make([]byte, 32)),
	} {
		if IsSeedReveal(text, pubHex) {
			t.Fatalf("must not announce: %q", text)
		}
	}
	if IsSeedReveal(seedHex, "zz") {
		t.Fatal("bad pub must not announce")
	}
}

package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"testing"
	"time"

	"github.com/CleveTok3125/V2V/internal/pow"
	"github.com/CleveTok3125/V2V/internal/wire"
)

func signTestOffer(t *testing.T, o wire.PowOffer) (string, string) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	po := pow.Offer{
		Tier: o.Tier,
		Preset: pow.Preset{
			Time: o.T, Memory: o.M, Threads: o.P, Difficulty: o.Difficulty,
		},
		Salt: o.Salt, Expires: o.Expires, ChallengeID: o.ChallengeID,
	}
	sig := hex.EncodeToString(pow.SignOffer(priv, po))
	return sig, hex.EncodeToString(pub)
}

func testPowOffer() wire.PowOffer {
	return wire.PowOffer{
		Type: "pow_offer", Tier: 1,
		T: 1, M: 8192, P: 1, Difficulty: 8,
		Salt: "s", Expires: time.Now().Add(time.Minute).Unix(), ChallengeID: "c1",
	}
}

func TestVerifyPowOfferPinned(t *testing.T) {
	o := testPowOffer()
	sig, pub := signTestOffer(t, o)
	o.OfferSig, o.ServerPub = sig, pub
	preset, err := verifyPowOffer(o, pub, false)
	if err != nil {
		t.Fatal(err)
	}
	if preset.Time != 1 || preset.Memory != 8192 || preset.Difficulty != 8 {
		t.Fatalf("preset mismatch: %+v", preset)
	}
}

func TestVerifyPowOfferTamper(t *testing.T) {
	o := testPowOffer()
	sig, pub := signTestOffer(t, o)
	o.OfferSig, o.ServerPub = sig, pub
	o.Difficulty = 4
	if _, err := verifyPowOffer(o, pub, false); err == nil {
		t.Fatal("tampered difficulty must fail with pin")
	}
}

func TestVerifyPowOfferTOFU(t *testing.T) {
	o := testPowOffer()
	if _, err := verifyPowOffer(o, "", false); err != nil {
		t.Fatalf("no pin must proceed TOFU: %v", err)
	}
}

func TestVerifyPowOfferWasmClamp(t *testing.T) {
	o := testPowOffer()
	o.P = 4
	sig, pub := signTestOffer(t, o)
	o.OfferSig, o.ServerPub = sig, pub
	preset, err := verifyPowOffer(o, pub, true)
	if err != nil {
		t.Fatal(err)
	}
	if preset.Threads != 1 {
		t.Fatalf("wasm must clamp P=1, got %+v", preset)
	}
}

func TestDecideOfferTier(t *testing.T) {
	if decideOffer(4, 3) {
		t.Fatal("tier above max must decline")
	}
	if !decideOffer(2, 3) {
		t.Fatal("tier within max must accept")
	}
	if !decideOffer(3, 3) {
		t.Fatal("tier equal to max must accept")
	}
}

func TestSolveWithBudget(t *testing.T) {
	p := pow.Preset{Time: 1, Memory: 8 * 1024, Threads: 1, Difficulty: 8}
	nonce, err := solveWithBudget(p, "budget-salt", 60_000)
	if err != nil {
		t.Fatal(err)
	}
	if !pow.Verify(p, "budget-salt", nonce) {
		t.Fatal("solution must verify")
	}
}

func TestSolveWithBudgetTimeout(t *testing.T) {
	p := pow.Preset{Time: 1, Memory: 8 * 1024, Threads: 1, Difficulty: 40}
	if _, err := solveWithBudget(p, "budget-salt-2", 5); err == nil {
		t.Fatal("impossible solve must time out")
	}
}

func TestGatePassURL(t *testing.T) {
	for in, want := range map[string]string{
		"ws://h:1/ws":   "http://h:1",
		"wss://h:1/ws":  "https://h:1",
		"wss://h/x/ws":  "https://h/x",
		"ws://h":        "http://h",
		"http://h:1/ws": "http://h:1",
	} {
		if got := gateHTTPBase(in); got != want {
			t.Fatalf("gateHTTPBase(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseGateChallenge(t *testing.T) {
	body := []byte(`{"challenge_id":"c","tier":1,"pow":{"t":1,"m":8192,"p":1,"difficulty":16},"salt":"s","earliest":1700000000,"expires":1700000600,"ticket":"tkt","offer_sig":"sig","server_pub":"pub"}`)
	c, err := parseGateChallenge(body)
	if err != nil {
		t.Fatal(err)
	}
	if c.ID != "c" || c.Tier != 1 || c.Preset.Difficulty != 16 || c.Ticket != "tkt" {
		t.Fatalf("parse mismatch: %+v", c)
	}
	if _, err := parseGateChallenge([]byte(`{"tier":`)); err == nil {
		t.Fatal("malformed JSON must fail")
	}
}

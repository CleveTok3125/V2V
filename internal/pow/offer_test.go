package pow

import (
	"crypto/ed25519"
	"crypto/rand"
	"testing"
	"time"
)

func testOffer() (Offer, ed25519.PublicKey, ed25519.PrivateKey) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		panic(err)
	}
	return Offer{
		Tier:        2,
		Preset:      Preset{Time: 1, Memory: 8 * 1024, Threads: 1, Difficulty: 12},
		Salt:        "offer-salt-v1",
		Expires:     time.Now().Add(time.Minute).Unix(),
		ChallengeID: "ch-1",
	}, pub, priv
}

func TestOfferSignVerify(t *testing.T) {
	o, pub, priv := testOffer()
	sig := SignOffer(priv, o)
	if !VerifyOffer(pub, o, sig) {
		t.Fatal("fresh offer must verify")
	}
}

func TestOfferTamper(t *testing.T) {
	o, pub, priv := testOffer()
	sig := SignOffer(priv, o)
	mut := o
	mut.Preset.Difficulty = 4
	if VerifyOffer(pub, mut, sig) {
		t.Fatal("lowered difficulty must fail")
	}
	mut = o
	mut.Tier = 1
	if VerifyOffer(pub, mut, sig) {
		t.Fatal("changed tier must fail")
	}
	mut = o
	mut.Salt = "other"
	if VerifyOffer(pub, mut, sig) {
		t.Fatal("changed salt must fail")
	}
	otherPub, _, _ := ed25519.GenerateKey(rand.Reader)
	if VerifyOffer(otherPub, o, sig) {
		t.Fatal("wrong pubkey must fail")
	}
	if len(sig) != ed25519.SignatureSize {
		t.Fatalf("sig must be %d bytes", ed25519.SignatureSize)
	}
}

func TestOfferExpiry(t *testing.T) {
	o, _, _ := testOffer()
	if o.Expired(time.Now()) {
		t.Fatal("future offer must not be expired")
	}
	past := o
	past.Expires = time.Now().Add(-time.Second).Unix()
	if !past.Expired(time.Now()) {
		t.Fatal("past offer must be expired")
	}
}

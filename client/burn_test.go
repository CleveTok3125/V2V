package main

import (
	"crypto/ed25519"
	"encoding/hex"
	"strings"
	"testing"
)

func burnFixture() (seedHex, pubHex string, priv ed25519.PrivateKey) {
	seed := make([]byte, 32)
	for i := range seed {
		seed[i] = byte(i + 5)
	}
	priv = ed25519.NewKeyFromSeed(seed)
	pub := priv.Public().(ed25519.PublicKey)
	return hex.EncodeToString(seed), hex.EncodeToString(pub), priv
}

func TestCmdBurnWarnsFirst(t *testing.T) {
	sess, out := behaviorSession(t)
	seedHex, _, priv := burnFixture()
	sess.TripPriv = priv
	sess.TripPub = priv.Public().(ed25519.PublicKey)
	sess.TripBadge = "◆ deadbeef"
	if !sess.cmdBurn("/burn") {
		t.Fatal("/burn must match")
	}
	got := out.String()
	if !strings.Contains(got, "/burn confirm") {
		t.Fatalf("burn must ask for confirm: %q", got)
	}
	if strings.Contains(got, seedHex) {
		t.Fatalf("burn must not show the seed before confirm: %q", got)
	}
	if _, act := sess.dispatch("/burn"); act != cmdDone {
		t.Fatalf("dispatch /burn act=%v, want cmdDone", act)
	}
}

func TestCmdBurnConfirmShowsSeed(t *testing.T) {
	sess, out := behaviorSession(t)
	seedHex, pubHex, priv := burnFixture()
	sess.TripPriv = priv
	sess.TripPub = priv.Public().(ed25519.PublicKey)
	sess.TripBadge = "◆ deadbeef"
	if !sess.cmdBurn("/burn confirm") {
		t.Fatal("/burn confirm must match")
	}
	got := out.String()
	if !strings.Contains(got, seedHex) || !strings.Contains(got, pubHex) {
		t.Fatalf("confirm must show seed and pub: %q", got)
	}
}

func TestCmdBurnWithoutTrip(t *testing.T) {
	sess, out := behaviorSession(t)
	if !sess.cmdBurn("/burn") {
		t.Fatal("/burn must match")
	}
	if got := out.String(); !strings.Contains(got, "không dùng tripcode") {
		t.Fatalf("burn without trip must report: %q", got)
	}
	sess, out = behaviorSession(t)
	if !sess.cmdBurn("/burn confirm") {
		t.Fatal("/burn confirm must match")
	}
	if got := out.String(); !strings.Contains(got, "không dùng tripcode") {
		t.Fatalf("confirm without trip must report: %q", got)
	}
}

func TestCmdBurnVerify(t *testing.T) {
	seedHex, pubHex, _ := burnFixture()
	sess, out := behaviorSession(t)
	if !sess.cmdBurnVerify("/burn-verify " + seedHex + " " + pubHex) {
		t.Fatal("/burn-verify must match")
	}
	if got := out.String(); !strings.Contains(got, "đã lộ") {
		t.Fatalf("matching seed must report exposed: %q", got)
	}
	sess, out = behaviorSession(t)
	sess.cmdBurnVerify("/burn-verify " + seedHex + " 00" + pubHex[2:])
	if got := out.String(); !strings.Contains(got, "không khớp") {
		t.Fatalf("wrong pub must report mismatch: %q", got)
	}
	sess, out = behaviorSession(t)
	if !sess.cmdBurnVerify("/burn-verify onlyone") {
		t.Fatal("/burn-verify must match with bad args")
	}
	if out.Len() == 0 {
		t.Fatal("bad args must explain usage")
	}
}

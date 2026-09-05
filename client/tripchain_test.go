package main

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

func TestDeriveTripKeyDeterministic(t *testing.T) {
	priv1, pub1, badge1 := deriveTripKey("hello world", "deadbeef00112233445566778899aabbccddeeff00112233445566778899aabbcc")
	priv2, pub2, badge2 := deriveTripKey("hello world", "deadbeef00112233445566778899aabbccddeeff00112233445566778899aabbcc")
	if hex.EncodeToString(pub1) != hex.EncodeToString(pub2) {
		t.Fatalf("same passphrase+pub should give same pub: %x vs %x", pub1, pub2)
	}
	if badge1 != badge2 {
		t.Fatalf("badge mismatch %s vs %s", badge1, badge2)
	}
	if len(priv1) != ed25519.PrivateKeySize || len(priv2) != ed25519.PrivateKeySize {
		t.Fatalf("priv size wrong")
	}
	// Different serverPub should give different key
	_, pub3, _ := deriveTripKey("hello world", "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff")
	if hex.EncodeToString(pub1) == hex.EncodeToString(pub3) {
		t.Fatalf("different serverPub should give different pub")
	}
	// Different passphrase
	_, pub4, _ := deriveTripKey("different", "deadbeef00112233445566778899aabbccddeeff00112233445566778899aabbcc")
	if hex.EncodeToString(pub1) == hex.EncodeToString(pub4) {
		t.Fatalf("different passphrase should give different pub")
	}
}

func TestDeriveTripBadge(t *testing.T) {
	_, pub, badge := deriveTripKey("test", "ab12")
	h := sha256.Sum256(pub)
	expect := hex.EncodeToString(h[:])[:8]
	if badge != expect {
		t.Fatalf("badge %s != expected %s", badge, expect)
	}
	if len(badge) != 8 {
		t.Fatalf("badge length %d", len(badge))
	}
}

func TestCanonicalPayloadDeterministic(t *testing.T) {
	serverPub := "aa" + hex.EncodeToString(make([]byte, 31))
	prev := make([]byte, 32)
	msgHash := sha256.Sum256([]byte("hello"))
	pub := make([]byte, 32)
	for i := range pub {
		pub[i] = byte(i)
	}
	displayName := "Tester#eff8"
	a := canonicalPayload(serverPub, 1, prev, msgHash[:], pub, displayName, 9, 0)
	b := canonicalPayload(serverPub, 1, prev, msgHash[:], pub, displayName, 9, 0)
	if string(a) != string(b) {
		t.Fatalf("canonical payload not deterministic")
	}
	// Different seq should differ
	c := canonicalPayload(serverPub, 2, prev, msgHash[:], pub, displayName, 9, 0)
	if string(a) == string(c) {
		t.Fatalf("different seq should differ")
	}
	// Different displayName should differ
	d := canonicalPayload(serverPub, 1, prev, msgHash[:], pub, "Other#1234", 9, 0)
	e := canonicalPayload(serverPub, 1, prev, msgHash[:], pub, displayName, 10, 0)
	if string(a) == string(e) {
		t.Fatalf("different tmp_id should differ")
	}
	if string(a) == string(d) {
		t.Fatalf("different displayName should differ")
	}
}

func TestTripSigningRoundtrip(t *testing.T) {
	priv, pub, _ := deriveTripKey("passphrase for signing", "serverpubhex00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff00112233")
	serverPub := "serverpubhex00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff00112233"
	prev := make([]byte, 32)
	msg := "hello world"
	msgHash := sha256.Sum256([]byte(msg))
	displayName := "Tester#eff8"
	payload := canonicalPayload(serverPub, 1, prev, msgHash[:], pub, displayName, 9, 0)
	sig := ed25519.Sign(priv, payload)
	if !ed25519.Verify(pub, payload, sig) {
		t.Fatalf("signature should verify")
	}
	// Tamper message hash should fail
	badHash := sha256.Sum256([]byte("tampered"))
	badPayload := canonicalPayload(serverPub, 1, prev, badHash[:], pub, displayName, 9, 0)
	if ed25519.Verify(pub, badPayload, sig) {
		t.Fatalf("tampered payload should not verify")
	}
	// Different serverPub should fail
	badPayload2 := canonicalPayload("differentServerPub", 1, prev, msgHash[:], pub, displayName, 9, 0)
	if ed25519.Verify(pub, badPayload2, sig) {
		t.Fatalf("different serverPub should not verify")
	}
	// Different displayName should fail
	badPayload3 := canonicalPayload(serverPub, 1, prev, msgHash[:], pub, "Evil#1234", 9, 0)
	if ed25519.Verify(pub, badPayload3, sig) {
		t.Fatalf("different displayName should not verify")
	}
}

func TestTripHashChain(t *testing.T) {
	priv, pub, _ := deriveTripKey("chain test", "server1234567890abcdef1234567890abcdef1234567890abcdef1234567890ab")
	serverPub := "server1234567890abcdef1234567890abcdef1234567890abcdef1234567890ab"
	prev := make([]byte, 32)
	seq := uint32(1)
	displayName := "Tester#eff8"
	// Simulate 3 messages
	for i := 0; i < 3; i++ {
		msg := "msg " + string(rune('a'+i))
		msgHash := sha256.Sum256([]byte(msg))
		payload := canonicalPayload(serverPub, seq, prev, msgHash[:], pub, displayName, uint64(i+1), 0)
		sig := ed25519.Sign(priv, payload)
		if !ed25519.Verify(pub, payload, sig) {
			t.Fatalf("msg %d verify failed", i)
		}
		// chain prev = sha256(prev|sig|msgHash)
		h := sha256.New()
		h.Write(prev)
		h.Write(sig)
		h.Write(msgHash[:])
		prev = h.Sum(nil)
		seq++
	}
	if seq != 4 {
		t.Fatalf("seq should be 4, got %d", seq)
	}
}

func TestTripMessageJSON(t *testing.T) {
	m := TripMessage{Text: "hello", Pub: "ab12", Seq: 5, Prev: "00", Sig: "ff"}
	if m.GetText() != "hello" {
		t.Fatalf("GetText failed")
	}
	m2 := TripMessage{Msg: "fallback", Pub: "ab12"}
	if m2.GetText() != "fallback" {
		t.Fatalf("GetText fallback failed")
	}
}

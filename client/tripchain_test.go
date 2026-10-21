package main

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"github.com/CleveTok3125/V2V/lib/config"
	"github.com/CleveTok3125/V2V/lib/tripcolor"
)

func TestDeriveTripKeyDeterministic(t *testing.T) {
	clearTripSalt(t)
	srv := AuthPacket{ServerPubKey: "deadbeef00112233445566778899aabbccddeeff00112233445566778899aabbcc"}
	def := tripServerDefault(srv)
	priv1, pub1, badge1 := deriveTripKey("hello world", resolveTripSalt("", def))
	priv2, pub2, badge2 := deriveTripKey("hello world", resolveTripSalt("", def))
	if hex.EncodeToString(pub1) != hex.EncodeToString(pub2) {
		t.Fatalf("same passphrase+salt should give same pub: %x vs %x", pub1, pub2)
	}
	if badge1 != badge2 {
		t.Fatalf("badge mismatch %s vs %s", badge1, badge2)
	}
	if len(priv1) != ed25519.PrivateKeySize || len(priv2) != ed25519.PrivateKeySize {
		t.Fatalf("priv size wrong")
	}
	// Different server default gives different key.
	other := tripServerDefault(AuthPacket{ServerPubKey: "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"})
	_, pub3, _ := deriveTripKey("hello world", resolveTripSalt("", other))
	if hex.EncodeToString(pub1) == hex.EncodeToString(pub3) {
		t.Fatalf("different serverPub should give different pub")
	}
	_, pub4, _ := deriveTripKey("different", resolveTripSalt("", def))
	if hex.EncodeToString(pub1) == hex.EncodeToString(pub4) {
		t.Fatalf("different passphrase should give different pub")
	}
}

func TestDeriveTripKeyCustomSaltPortable(t *testing.T) {
	clearTripSalt(t)
	t.Setenv("V2V_TRIP_SALT", "my-fixed-domain")
	defA := tripServerDefault(AuthPacket{ServerPubKey: "deadbeef00112233445566778899aabbccddeeff00112233445566778899aabbcc"})
	defB := tripServerDefault(AuthPacket{ServerPubKey: "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"})
	_, pubA, _ := deriveTripKey("hello world", resolveTripSalt(tripCustomSalt(), defA))
	_, pubB, _ := deriveTripKey("hello world", resolveTripSalt(tripCustomSalt(), defB))
	if hex.EncodeToString(pubA) != hex.EncodeToString(pubB) {
		t.Fatalf("custom salt should give same pub on every server")
	}
}

// clearTripSalt pins the salt inputs to server-default for tests: no
// env override and no config override (restored afterwards).
func clearTripSalt(t *testing.T) {
	t.Helper()
	t.Setenv("V2V_TRIP_SALT", "")
	old := ClientCfg
	t.Cleanup(func() { ClientCfg = old })
	if ClientCfg == nil {
		ClientCfg = config.DefaultClientConfig()
	}
	ClientCfg.Crypto.Salt = ""
}

func TestResolveTripSalt(t *testing.T) {
	serverPub := "ab12ab12ab12ab12ab12ab12ab12ab12ab12ab12ab12ab12ab12ab12ab12ab12"
	server := tripServerDefault(AuthPacket{ServerPubKey: serverPub})
	custom := resolveTripSalt("mine", server)
	if string(custom) == string(server) {
		t.Fatal("custom salt must differ from server default")
	}
	if string(resolveTripSalt("mine", server)) != string(resolveTripSalt("mine", tripServerDefault(AuthPacket{}))) {
		t.Fatal("custom salt must ignore server default")
	}
	// Advertised TRIP_SALT wins over serverPub.
	adv := tripServerDefault(AuthPacket{ServerPubKey: serverPub, TripSalt: "op-domain"})
	if string(adv) == string(server) {
		t.Fatal("advertised trip salt must differ from serverPub-derived")
	}
	if string(tripServerDefault(AuthPacket{ServerPubKey: "ff", TripSalt: "op-domain"})) != string(adv) {
		t.Fatal("advertised trip salt must ignore serverPub")
	}
	def := tripServerDefault(AuthPacket{})
	if len(def) != 16 {
		t.Fatalf("fallback salt length = %d, want 16", len(def))
	}
	if string(tripServerDefault(AuthPacket{ServerPubKey: "zzzz"})) != string(def) {
		t.Fatal("invalid serverPub hex must fall back to fixed salt")
	}
}

func TestDeriveTripBadge(t *testing.T) {
	clearTripSalt(t)
	def := tripServerDefault(AuthPacket{ServerPubKey: "ab12"})
	_, pub, badge := deriveTripKey("test", resolveTripSalt("", def))
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
	a := tripcolor.CanonicalPayload(serverPub, 1, prev, msgHash[:], pub, displayName, 9, 0)
	b := tripcolor.CanonicalPayload(serverPub, 1, prev, msgHash[:], pub, displayName, 9, 0)
	if string(a) != string(b) {
		t.Fatalf("canonical payload not deterministic")
	}
	// Different seq should differ
	c := tripcolor.CanonicalPayload(serverPub, 2, prev, msgHash[:], pub, displayName, 9, 0)
	if string(a) == string(c) {
		t.Fatalf("different seq should differ")
	}
	// Different displayName should differ
	d := tripcolor.CanonicalPayload(serverPub, 1, prev, msgHash[:], pub, "Other#1234", 9, 0)
	e := tripcolor.CanonicalPayload(serverPub, 1, prev, msgHash[:], pub, displayName, 10, 0)
	if string(a) == string(e) {
		t.Fatalf("different tmp_id should differ")
	}
	if string(a) == string(d) {
		t.Fatalf("different displayName should differ")
	}
}

func TestTripSigningRoundtrip(t *testing.T) {
	clearTripSalt(t)
	def := tripServerDefault(AuthPacket{})
	priv, pub, _ := deriveTripKey("passphrase for signing", resolveTripSalt("sign-domain", def))
	serverPub := "aa" + hex.EncodeToString(make([]byte, 31))
	prev := make([]byte, 32)
	msg := "hello world"
	msgHash := sha256.Sum256([]byte(msg))
	displayName := "Tester#eff8"
	payload := tripcolor.CanonicalPayload(serverPub, 1, prev, msgHash[:], pub, displayName, 9, 0)
	sig := ed25519.Sign(priv, payload)
	if !ed25519.Verify(pub, payload, sig) {
		t.Fatalf("signature should verify")
	}
	// Tamper message hash should fail
	badHash := sha256.Sum256([]byte("tampered"))
	badPayload := tripcolor.CanonicalPayload(serverPub, 1, prev, badHash[:], pub, displayName, 9, 0)
	if ed25519.Verify(pub, badPayload, sig) {
		t.Fatalf("tampered payload should not verify")
	}
	// Different serverPub should fail
	badPayload2 := tripcolor.CanonicalPayload("differentServerPub", 1, prev, msgHash[:], pub, displayName, 9, 0)
	if ed25519.Verify(pub, badPayload2, sig) {
		t.Fatalf("different serverPub should not verify")
	}
	// Different displayName should fail
	badPayload3 := tripcolor.CanonicalPayload(serverPub, 1, prev, msgHash[:], pub, "Evil#1234", 9, 0)
	if ed25519.Verify(pub, badPayload3, sig) {
		t.Fatalf("different displayName should not verify")
	}
}

func TestTripHashChain(t *testing.T) {
	clearTripSalt(t)
	def := tripServerDefault(AuthPacket{})
	priv, pub, _ := deriveTripKey("chain test", resolveTripSalt("chain-domain", def))
	serverPub := "aa" + hex.EncodeToString(make([]byte, 31))
	prev := make([]byte, 32)
	seq := uint32(1)
	displayName := "Tester#eff8"
	// Simulate 3 messages
	for i := 0; i < 3; i++ {
		msg := "msg " + string(rune('a'+i))
		msgHash := sha256.Sum256([]byte(msg))
		payload := tripcolor.CanonicalPayload(serverPub, seq, prev, msgHash[:], pub, displayName, uint64(i+1), 0)
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

// BenchmarkDeriveTripKey calibrates the passphrase threshold math: one
// offline guess costs roughly this long (native argon2id t=3/m=64MB),
// so score<=1 (<1e6 guesses) means days of single-CPU work.
func BenchmarkDeriveTripKey(b *testing.B) {
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		priv, _, _ := deriveTripKey("con meo ngu trua", resolveTripSalt("", tripServerDefault(AuthPacket{})))
		for j := range priv {
			priv[j] = 0
		}
	}
}

package main

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"testing"

	"github.com/fxamacker/cbor/v2"
)

const (
	testRPID   = "chat.example.com"
	testOrigin = "https://chat.example.com"
	testNonce  = "deadbeefcafebabe0123456789abcdef" // hex nonce as issued
)

func setupWA(t *testing.T) {
	t.Helper()
	WAConfig = webauthnConfig{RPID: testRPID, Origin: testOrigin, Enabled: true}
	t.Cleanup(func() { WAConfig = webauthnConfig{} })
}

// buildAuthData returns the authenticatorData blob: rpIdHash || flags || counter.
func buildAuthData(t *testing.T) []byte {
	t.Helper()
	rp := sha256.Sum256([]byte(testRPID))
	out := make([]byte, 37)
	copy(out[:32], rp[:])
	out[32] = authDataFlagUP // UP set, no AT/ED
	return out
}

// buildClientData marshals the CollectedClientData JSON with the challenge
// derived from the nonce, exactly like a browser would.
func buildClientData(t *testing.T, nonceHex string) []byte {
	t.Helper()
	cd := clientData{
		Type:      "webauthn.get",
		Challenge: base64.RawURLEncoding.EncodeToString(ChallengeFromNonce(nonceHex)),
		Origin:    testOrigin,
	}
	b, err := json.Marshal(cd)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func coseEC2(t *testing.T, pub *ecdsa.PublicKey) []byte {
	t.Helper()
	m := map[int]any{
		1:  int64(2),             // kty: EC2
		3:  int64(coseAlgES256),  // alg: ES256
		-1: int64(1),             // crv: P-256
		-2: pad32(pub.X.Bytes()), // x
		-3: pad32(pub.Y.Bytes()), // y
	}
	b, err := cbor.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func coseRSA(t *testing.T, pub *rsa.PublicKey) []byte {
	t.Helper()
	m := map[int]any{
		1:  int64(3),                         // kty: RSA
		3:  int64(coseAlgRS256),              // alg: RS256
		-1: pub.N.Bytes(),                    // n
		-2: big.NewInt(int64(pub.E)).Bytes(), // e
	}
	b, err := cbor.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func pad32(b []byte) []byte {
	if len(b) >= 32 {
		return b
	}
	out := make([]byte, 32)
	copy(out[32-len(b):], b)
	return out
}

func signPayload(t *testing.T, priv any, signed []byte) []byte {
	t.Helper()
	digest := sha256.Sum256(signed)
	switch k := priv.(type) {
	case *ecdsa.PrivateKey:
		sig, err := ecdsa.SignASN1(rand.Reader, k, digest[:])
		if err != nil {
			t.Fatal(err)
		}
		return sig
	case *rsa.PrivateKey:
		sig, err := rsa.SignPKCS1v15(rand.Reader, k, crypto.SHA256, digest[:])
		if err != nil {
			t.Fatal(err)
		}
		return sig
	default:
		t.Fatal("unknown key type")
		return nil
	}
}

func TestVerifyAssertionES256HappyPath(t *testing.T) {
	setupWA(t)
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	authData := buildAuthData(t)
	cd := buildClientData(t, testNonce)
	cdHash := sha256.Sum256(cd)
	signed := append(append([]byte{}, authData...), cdHash[:]...)
	sig := signPayload(t, priv, signed)

	err = verifyAssertion(
		coseEC2(t, &priv.PublicKey), testNonce,
		base64.RawURLEncoding.EncodeToString(authData),
		base64.RawURLEncoding.EncodeToString(cd),
		base64.RawURLEncoding.EncodeToString(sig),
	)
	if err != nil {
		t.Fatalf("happy path failed: %v", err)
	}
}

func TestVerifyAssertionRS256(t *testing.T) {
	setupWA(t)
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	authData := buildAuthData(t)
	cd := buildClientData(t, testNonce)
	cdHash := sha256.Sum256(cd)
	signed := append(append([]byte{}, authData...), cdHash[:]...)
	sig := signPayload(t, priv, signed)

	if err := verifyAssertion(
		coseRSA(t, &priv.PublicKey), testNonce,
		base64.RawURLEncoding.EncodeToString(authData),
		base64.RawURLEncoding.EncodeToString(cd),
		base64.RawURLEncoding.EncodeToString(sig),
	); err != nil {
		t.Fatalf("RS256 happy path failed: %v", err)
	}
}

func TestVerifyAssertionTampering(t *testing.T) {
	setupWA(t)
	priv, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	pubCOSE := coseEC2(t, &priv.PublicKey)
	authData := buildAuthData(t)
	cd := buildClientData(t, testNonce)
	cdHash := sha256.Sum256(cd)
	signed := append(append([]byte{}, authData...), cdHash[:]...)
	sig := base64.RawURLEncoding.EncodeToString(signPayload(t, priv, signed))
	adB64 := base64.RawURLEncoding.EncodeToString(authData)
	cdB64 := base64.RawURLEncoding.EncodeToString(cd)

	// wrong nonce (challenge mismatch)
	if err := verifyAssertion(pubCOSE, "ffffff"+testNonce[6:], adB64, cdB64, sig); err == nil {
		t.Error("challenge mismatch accepted")
	}
	// wrong origin
	WAConfig.Origin = "https://evil.example.com"
	cdBad := buildClientData(t, testNonce)
	if err := verifyAssertion(pubCOSE, testNonce, adB64,
		base64.RawURLEncoding.EncodeToString(cdBad), sig); err == nil {
		t.Error("origin mismatch accepted")
	}
	WAConfig.Origin = testOrigin
	// flipped signature byte
	sigBytes := []byte(sig)
	if sigBytes[10] == 'A' {
		sigBytes[10] = 'B'
	} else {
		sigBytes[10] = 'A'
	}
	if err := verifyAssertion(pubCOSE, testNonce, adB64, cdB64, string(sigBytes)); err == nil {
		t.Error("tampered signature accepted")
	}
	// user-present flag cleared
	badAuth := append([]byte{}, authData...)
	badAuth[32] = 0
	if err := verifyAssertion(pubCOSE, testNonce,
		base64.RawURLEncoding.EncodeToString(badAuth), cdB64, sig); err == nil {
		t.Error("missing UP flag accepted")
	}
	// wrong rpId in config
	WAConfig.RPID = "other.example.com"
	rpChanged := sha256.Sum256([]byte(WAConfig.RPID))
	_ = rpChanged
	if err := verifyAssertion(pubCOSE, testNonce, adB64, cdB64, sig); err == nil {
		t.Error("rp id hash mismatch accepted")
	}
}

func TestChallengeFromNonceDeterministic(t *testing.T) {
	a := ChallengeFromNonce(testNonce)
	b := ChallengeFromNonce(testNonce)
	if string(a) != string(b) || len(a) != 32 {
		t.Fatal("challenge derivation not deterministic 32-byte")
	}
}

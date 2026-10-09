package main

// A software authenticator for tests. The server and the client cannot
// mint one of these — a real credential only ever enters through a browser
// ceremony — so it lives in _test.go and exists purely to drive the real
// WebAuthn verification path without a browser.

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"testing"
	"time"

	"github.com/fxamacker/cbor/v2"
)

type testPasskey struct {
	key     *ecdsa.PrivateKey
	credID  string
	counter uint32
}

func (p *testPasskey) credentialID() string { return p.credID }

// coseKey encodes an ES256 public key the way an authenticator would hand
// it to a relying party: a CBOR map keyed by the COSE labels.
func coseKey(pub *ecdsa.PublicKey) ([]byte, error) {
	x := make([]byte, 32)
	y := make([]byte, 32)
	pub.X.FillBytes(x)
	pub.Y.FillBytes(y)
	return cbor.Marshal(map[int]any{
		1:  2,  // kty: EC2
		3:  -7, // alg: ES256
		-1: 1,  // crv: P-256
		-2: x,  // x coordinate
		-3: y,  // y coordinate
	})
}

// assert produces the fields a browser would POST after
// navigator.credentials.get over the given challenge source. The challenge is
// derived exactly as the shipped page derives it: SHA-256 over the ticket
// string, so the test would break if that mapping ever changed.
func (p *testPasskey) assert(t *testing.T, ticket string) map[string]any {
	t.Helper()
	p.counter++
	rpHash := sha256.Sum256([]byte(WAConfig.RPID))

	// UP | UV: both flags the server requires.
	authData := append([]byte{}, rpHash[:]...)
	authData = append(authData, 0x01|0x04)
	authData = append(authData,
		byte(p.counter>>24), byte(p.counter>>16), byte(p.counter>>8), byte(p.counter))

	clientData := `{"type":"webauthn.get","challenge":"` +
		base64.RawURLEncoding.EncodeToString(ChallengeFromNonce(ticket)) +
		`","origin":"` + WAConfig.Origin + `","crossOrigin":false}`
	clientHash := sha256.Sum256([]byte(clientData))

	signed := append(append([]byte{}, authData...), clientHash[:]...)
	digest := sha256.Sum256(signed)
	sig, err := ecdsa.SignASN1(rand.Reader, p.key, digest[:])
	if err != nil {
		t.Fatal(err)
	}

	return map[string]any{
		"auth_data":   base64.RawURLEncoding.EncodeToString(authData),
		"client_data": base64.RawURLEncoding.EncodeToString([]byte(clientData)),
		"sig":         base64.RawURLEncoding.EncodeToString(sig),
	}
}

// newTestPasskey enrolls a credential through the store's real bookkeeping
// so the assertions below are verified against a credential that arrived the
// way a real one does.
func newTestPasskey(t *testing.T, store *WebAuthnStore, role string) *testPasskey {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	pub, err := coseKey(&key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	rawID := make([]byte, 32)
	if _, err := rand.Read(rawID); err != nil {
		t.Fatal(err)
	}
	credID := base64.RawURLEncoding.EncodeToString(rawID)

	ticket, err := store.CreatePendingTicket(role, "test", 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	err = store.CompleteEnrollment(ticket, &WAStoredCred{
		CredentialID: credID,
		PublicKey:    base64.RawURLEncoding.EncodeToString(pub),
		SignCount:    0,
		AttFormat:    "none",
	})
	if err != nil {
		t.Fatal(err)
	}
	return &testPasskey{key: key, credID: credID}
}

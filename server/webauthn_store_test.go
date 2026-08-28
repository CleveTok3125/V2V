package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/fxamacker/cbor/v2"
)

func newTestStore(t *testing.T) *WebAuthnStore {
	t.Helper()
	return NewWebAuthnStore(filepath.Join(t.TempDir(), "webauthn.json"))
}

func TestTicketLifecycle(t *testing.T) {
	s := newTestStore(t)
	code, err := s.CreatePendingTicket("member", "bob-laptop", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	role, err := s.BindChallenge(code, "Y2hhbGxlbmdl")
	if err != nil || role != "member" {
		t.Fatalf("begin failed: role=%q err=%v", role, err)
	}
	err = s.CompleteEnrollment(code, &WAStoredCred{
		CredentialID: "cid-1", PublicKey: "cose-blob", SignCount: 5,
	})
	if err != nil {
		t.Fatal(err)
	}
	// ticket is now consumed
	if _, err := s.BindChallenge(code, "again"); err == nil {
		t.Fatal("used ticket accepted at begin")
	}
	cred, ok := s.Credential("member", "cid-1")
	if !ok || cred.SignCount != 5 || cred.Label != "bob-laptop" {
		t.Fatalf("stored credential mismatch: %+v ok=%v", cred, ok)
	}
	// counter must increase
	if err := s.UpdateSignCount("member", "cid-1", 4); err == nil {
		t.Error("decreasing counter accepted")
	}
	if err := s.UpdateSignCount("member", "cid-1", 6); err != nil {
		t.Errorf("increasing counter rejected: %v", err)
	}
}

func TestExpiredTicketRejected(t *testing.T) {
	s := newTestStore(t)
	code, _ := s.CreatePendingTicket("member", "", -time.Minute) // already expired
	if _, err := s.BindChallenge(code, "x"); err == nil {
		t.Fatal("expired ticket accepted")
	}
	err := s.CompleteEnrollment(code, &WAStoredCred{CredentialID: "x"})
	if err == nil {
		t.Fatal("expired ticket accepted at finish")
	}
}

// buildAttestation assembles a browser-shaped registration payload.
func buildAttestation(t *testing.T, priv *ecdsa.PrivateKey, challengeB64 string, rpid, origin string) (clientDataB64, attObjB64 string, credID []byte) {
	t.Helper()
	cdJSON, _ := json.Marshal(map[string]any{
		"type":      "webauthn.create",
		"challenge": challengeB64,
		"origin":    origin,
	})
	rpHash := sha256.Sum256([]byte(rpid))
	credID = make([]byte, 32)
	copy(credID, rpHash[:]) // any stable bytes work for the test

	authData := make([]byte, 0, 37+16+2+len(credID)+200)
	authData = append(authData, rpHash[:]...)
	authData = append(authData, 0x41|0x40) // UP | AT
	authData = append(authData, 0, 0, 0, 0)
	authData = append(authData, make([]byte, 16)...) // aaguid zeros
	l := len(credID)
	authData = append(authData, byte(l>>8), byte(l))
	authData = append(authData, credID...)

	pub := priv.PublicKey
	cose, cerr := cbor.Marshal(map[int]any{
		1: int64(2), 3: int64(-7), -1: int64(1),
		-2: pad32(pub.X.Bytes()), -3: pad32(pub.Y.Bytes()),
	})
	if cerr != nil {
		t.Fatal(cerr)
	}
	authData = append(authData, cose...)

	attObj, cerr := cbor.Marshal(map[int]any{
		1: "none",
		2: authData,
		3: map[int]any{},
	})
	if cerr != nil {
		t.Fatal(cerr)
	}
	return base64.RawURLEncoding.EncodeToString(cdJSON),
		base64.RawURLEncoding.EncodeToString(attObj),
		credID
}

func TestParseCreationForImport(t *testing.T) {
	setupWA(t)
	priv, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	wantChal := base64.RawURLEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef"))
	cdB64, attB64, _ := buildAttestation(t, priv, wantChal, testRPID, testOrigin)

	parsed, err := parseCreationForImport(cdB64, attB64, wantChal)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if parsed.CredentialID == "" || len(parsed.PublicKey) == 0 {
		t.Fatal("empty parsed fields")
	}

	// wrong challenge rejected
	if _, err := parseCreationForImport(cdB64, attB64,
		base64.RawURLEncoding.EncodeToString([]byte("other-challenge-32-bytes!!!!!!"))); err == nil {
		t.Error("wrong challenge accepted")
	}
}

func TestAtomicWriteAndEnrollMerge(t *testing.T) {
	dir := t.TempDir()
	// Use real atomic write via webauthn store save
	s := NewWebAuthnStore(dir + "/webauthn.json")
	if _, err := s.CreatePendingTicket("member", "test", time.Minute); err != nil {
		t.Fatal(err)
	}
	if err := s.saveFile(&webauthnFile{Version: 1, Credentials: map[string][]*WAStoredCred{}}); err != nil {
		t.Fatal(err)
	}
	files, _ := filepath.Glob(dir + "/.tmp-*")
	if len(files) != 0 {
		t.Errorf("tmp files not cleaned: %v", files)
	}
}

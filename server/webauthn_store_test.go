package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
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

// A concurrent replay of the same assertion reads the same stored
// counter; only one UpdateSignCount may win so the passkey clone check
// cannot be bypassed by racing two requests.
func TestUpdateSignCount_ConcurrentReplayRejected(t *testing.T) {
	s := newTestStore(t)
	code, err := s.CreatePendingTicket("member", "lbl", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.BindChallenge(code, "Y2hhbGxlbmdl"); err != nil {
		t.Fatal(err)
	}
	if err := s.CompleteEnrollment(code, &WAStoredCred{CredentialID: "cid-c", SignCount: 5}); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	var okCount, errCount int32
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := s.UpdateSignCount("member", "cid-c", 6); err != nil {
				atomic.AddInt32(&errCount, 1)
			} else {
				atomic.AddInt32(&okCount, 1)
			}
		}()
	}
	wg.Wait()
	if okCount != 1 || errCount != 1 {
		t.Fatalf("ok=%d err=%d, want 1/1 so the replay loses", okCount, errCount)
	}
}

// The store version must match exactly: a fractional value such as 3.5
// must not truncate to v3 and be accepted.
func TestStore_RejectsNonIntegerVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "webauthn.json")
	if err := os.WriteFile(path, []byte(`{"version":3.5,"credentials":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	s := NewWebAuthnStore(path)
	if _, err := s.loadFile(); err == nil {
		t.Fatal("fractional version accepted")
	}
	if err := os.WriteFile(path, []byte(`{"version":3,"credentials":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.loadFile(); err != nil {
		t.Fatalf("exact v3 rejected: %v", err)
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
	authData = append(authData, 0x41|0x40|0x04) // UP | AT | UV
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

	attObj, cerr := cbor.Marshal(map[string]any{
		"fmt":      "none",
		"authData": authData,
		"attStmt":  map[string]any{},
	})
	if cerr != nil {
		t.Fatal(cerr)
	}
	return base64.RawURLEncoding.EncodeToString(cdJSON),
		base64.RawURLEncoding.EncodeToString(attObj),
		credID
}

func TestParseCreationLibRejects(t *testing.T) {
	setupWA(t)
	priv, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	wantChal := base64.RawURLEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef"))
	cdB64, attB64, credID := buildAttestation(t, priv, wantChal, testRPID, testOrigin)
	wantID := base64.RawURLEncoding.EncodeToString(credID)

	// fmt "none" with UV is accepted (attestation not required,
	// format recorded as-is); reject path is covered by the UV and
	// challenge cases in passkeys_test.go
	created, err := parseCreationLib(cdB64, attB64, wantChal, wantID)
	if err != nil {
		t.Fatalf("fmt none with UV rejected: %v", err)
	}
	if created.AttFormat != "none" {
		t.Fatalf("att format = %q, want none recorded", created.AttFormat)
	}
	// wrong challenge rejected
	if _, err := parseCreationLib(cdB64, attB64,
		base64.RawURLEncoding.EncodeToString([]byte("other-challenge-32-bytes!!!!!!")), wantID); err == nil {
		t.Error("wrong challenge accepted")
	}
	// malformed inputs fail closed
	if _, err := parseCreationLib("!!!", attB64, wantChal, wantID); err == nil {
		t.Error("malformed clientData accepted")
	}
}

func TestAtomicWriteAndEnrollMerge(t *testing.T) {
	dir := t.TempDir()
	// Use real atomic write via webauthn store save
	s := NewWebAuthnStore(dir + "/webauthn.json")
	if _, err := s.CreatePendingTicket("member", "test", time.Minute); err != nil {
		t.Fatal(err)
	}
	if err := s.saveFile(&webauthnFile{Version: 2, Credentials: map[string][]*WAStoredCred{}}); err != nil {
		t.Fatal(err)
	}
	files, _ := filepath.Glob(dir + "/.tmp-*")
	if len(files) != 0 {
		t.Errorf("tmp files not cleaned: %v", files)
	}
}

func TestOldStoreRefused(t *testing.T) {
	for _, v := range []string{`{"version":1,"credentials":{}}`, `{"version":2,"credentials":{}}`} {
		dir := t.TempDir()
		path := filepath.Join(dir, "webauthn.json")
		if err := os.WriteFile(path, []byte(v), 0o600); err != nil {
			t.Fatal(err)
		}
		s := NewWebAuthnStore(path)
		if err := s.view(func(f *webauthnFile) error { return nil }); err == nil {
			t.Fatalf("store %s must be refused", v[:14])
		} else if !strings.Contains(err.Error(), "enroll lại") {
			t.Fatalf("refusal must tell admin to re-enroll, got: %v", err)
		}
	}
}

func TestCredential_UnknownReturnsFalse(t *testing.T) {
	s := newTestStore(t)
	code, err := s.CreatePendingTicket("member", "bob-laptop", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CompleteEnrollment(code, &WAStoredCred{
		CredentialID: "cid-1", PublicKey: "cose-blob", SignCount: 5,
	}); err != nil {
		t.Fatal(err)
	}
	// Unknown credential ID under a known role must report not-found,
	// never dereference a nil credential.
	if cred, ok := s.Credential("member", "no-such-id"); ok || cred != nil {
		t.Fatalf("unknown credential accepted: %+v ok=%v", cred, ok)
	}
	// Unknown role must not panic either.
	if cred, ok := s.Credential("no-such-role", "cid-1"); ok || cred != nil {
		t.Fatalf("unknown role accepted: %+v ok=%v", cred, ok)
	}
}

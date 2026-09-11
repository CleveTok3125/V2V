package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"testing"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"

	"github.com/fxamacker/cbor/v2"
)

const (
	testRPID   = "chat.example.com"
	testOrigin = "https://chat.example.com"
	testNonce  = "deadbeefcafebabe0123456789abcdef" // hex nonce as issued
	testRole   = "member"
	testCredID = "dGVzdC1jcmVkZW50aWFsLWlkLTAxMjM0NTY" // base64url, 27 bytes
)

func setupWA(t *testing.T) {
	t.Helper()
	WAConfig = webauthnConfig{RPID: testRPID, Origin: testOrigin, Enabled: true}
	var err error
	WebAuth, err = webauthn.New(&webauthn.Config{
		RPDisplayName:         "V2V",
		RPID:                  testRPID,
		RPOrigins:             []string{testOrigin},
		AttestationPreference: protocol.PreferDirectAttestation,
		AuthenticatorSelection: protocol.AuthenticatorSelection{
			RequireResidentKey: protocol.ResidentKeyRequired(),
			ResidentKey:        protocol.ResidentKeyRequirementRequired,
			UserVerification:   protocol.VerificationRequired,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { WAConfig = webauthnConfig{}; WebAuth = nil })
}

// buildAuthData returns authenticatorData: rpIdHash || flags || counter.
func buildAuthData(t *testing.T, flags byte, counter uint32) []byte {
	t.Helper()
	rp := sha256.Sum256([]byte(testRPID))
	out := make([]byte, 37)
	copy(out[:32], rp[:])
	out[32] = flags
	out[33] = byte(counter >> 24)
	out[34] = byte(counter >> 16)
	out[35] = byte(counter >> 8)
	out[36] = byte(counter)
	return out
}

// buildClientData marshals CollectedClientData with the challenge derived
// from the nonce, exactly like a browser would.
func buildClientData(t *testing.T, typ, nonceHex, origin string) []byte {
	t.Helper()
	cd := map[string]any{
		"type":      typ,
		"challenge": base64.RawURLEncoding.EncodeToString(ChallengeFromNonce(nonceHex)),
		"origin":    origin,
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
		1:  int64(2),  // kty: EC2
		3:  int64(-7), // alg: ES256
		-1: int64(1),  // crv: P-256
		-2: pad32(pub.X.Bytes()),
		-3: pad32(pub.Y.Bytes()),
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

func signPayload(t *testing.T, priv *ecdsa.PrivateKey, signed []byte) []byte {
	t.Helper()
	digest := sha256.Sum256(signed)
	sig, err := ecdsa.SignASN1(rand.Reader, priv, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	return sig
}

const flagUPUV = 0x01 | 0x04

func loginArgs(t *testing.T, priv *ecdsa.PrivateKey, flags byte, counter uint32, nonceHex, origin string) (adB64, cdB64, sigB64 string) {
	t.Helper()
	authData := buildAuthData(t, flags, counter)
	cd := buildClientData(t, "webauthn.get", nonceHex, origin)
	cdHash := sha256.Sum256(cd)
	sig := signPayload(t, priv, append(append([]byte{}, authData...), cdHash[:]...))
	return base64.RawURLEncoding.EncodeToString(authData),
		base64.RawURLEncoding.EncodeToString(cd),
		base64.RawURLEncoding.EncodeToString(sig)
}

const flagUPUVBEBS = 0x01 | 0x04 | 0x08 | 0x10

func storedCred(t *testing.T, priv *ecdsa.PrivateKey, be, bs bool) *WAStoredCred {
	t.Helper()
	return &WAStoredCred{
		CredentialID:   testCredID,
		PublicKey:      base64.RawURLEncoding.EncodeToString(coseEC2(t, &priv.PublicKey)),
		BackupEligible: be,
		BackupState:    bs,
	}
}

func TestLibLoginHappyPath(t *testing.T) {
	setupWA(t)
	priv, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	ad, cd, sig := loginArgs(t, priv, flagUPUVBEBS, 7, testNonce, testOrigin)
	got, err := verifyAssertionLib(storedCred(t, priv, true, true), testNonce, ad, cd, sig, testRole)
	if err != nil {
		t.Fatalf("happy path failed: %v", err)
	}
	if got != 7 {
		t.Fatalf("counter = %d, want 7", got)
	}
}

func TestLibLoginBackupMismatch(t *testing.T) {
	setupWA(t)
	priv, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	ad, cd, sig := loginArgs(t, priv, flagUPUVBEBS, 1, testNonce, testOrigin)
	// Stored flags say non-backupable but the authenticator reports
	// backup-eligible: the exact production failure (synced provider
	// against zeroed stored flags) must reject, never silently pass.
	if _, err := verifyAssertionLib(storedCred(t, priv, false, false), testNonce, ad, cd, sig, testRole); err == nil {
		t.Fatal("backup-eligibility mismatch accepted")
	}
}

func TestLibLoginRejections(t *testing.T) {
	setupWA(t)
	priv, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	stored := storedCred(t, priv, true, true)
	ad, cd, sig := loginArgs(t, priv, flagUPUVBEBS, 1, testNonce, testOrigin)

	if _, err := verifyAssertionLib(stored, "ffffff"+testNonce[6:], ad, cd, sig, testRole); err == nil {
		t.Error("challenge mismatch accepted")
	}
	badCD := buildClientData(t, "webauthn.get", testNonce, "https://evil.example.com")
	if _, err := verifyAssertionLib(stored, testNonce, ad,
		base64.RawURLEncoding.EncodeToString(badCD), sig, testRole); err == nil {
		t.Error("origin mismatch accepted")
	}
	sigBytes := []byte(sig)
	if sigBytes[10] == 'A' {
		sigBytes[10] = 'B'
	} else {
		sigBytes[10] = 'A'
	}
	if _, err := verifyAssertionLib(stored, testNonce, ad, cd, string(sigBytes), testRole); err == nil {
		t.Error("tampered signature accepted")
	}
	noUP, _, _ := loginArgs(t, priv, 0x04|0x08|0x10, 1, testNonce, testOrigin)
	if _, err := verifyAssertionLib(stored, testNonce, noUP, cd, sig, testRole); err == nil {
		t.Error("missing UP flag accepted")
	}
	noUV, _, _ := loginArgs(t, priv, 0x01|0x08|0x10, 1, testNonce, testOrigin)
	if _, err := verifyAssertionLib(stored, testNonce, noUV, cd, sig, testRole); err == nil {
		t.Error("missing UV flag accepted")
	}
	// Unknown credential IDs never reach the adapter: auth.go only calls
	// it with the stored pubkey of a looked-up credential (store miss =
	// reject, covered by TestCredential_UnknownReturnsFalse). The
	// session allow-list binds this verification to the presented ID.
	if _, err := verifyAssertionLib(stored, testNonce, "!!!", cd, sig, testRole); err == nil {
		t.Error("malformed authData accepted")
	}
}

// buildCreationSelfAttested assembles a packed self-attested registration:
// fmt + attStmt signed by the credential key itself, UV per withUV.
func buildCreationSelfAttested(t *testing.T, priv *ecdsa.PrivateKey, challengeB64, rpid, origin string, flags byte) (cdB64, attB64, credID string) {
	t.Helper()
	cdJSON, _ := json.Marshal(map[string]any{
		"type":      "webauthn.create",
		"challenge": challengeB64,
		"origin":    origin,
	})
	rpHash := sha256.Sum256([]byte(rpid))
	idBytes := make([]byte, 32)
	if _, err := rand.Read(idBytes); err != nil {
		t.Fatal(err)
	}
	authData := append([]byte{}, rpHash[:]...)
	authData = append(authData, flags, 0, 0, 0, 0)
	authData = append(authData, make([]byte, 16)...)
	authData = append(authData, 0, 32)
	authData = append(authData, idBytes...)
	authData = append(authData, coseEC2(t, &priv.PublicKey)...)
	cdHash := sha256.Sum256(cdJSON)
	digest := sha256.Sum256(append(authData, cdHash[:]...))
	sig, err := ecdsa.SignASN1(rand.Reader, priv, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	attObj, err := cbor.Marshal(map[string]any{
		"fmt": "packed",
		"authData": authData,
		"attStmt":  map[string]any{"alg": int64(-7), "sig": sig},
	})
	if err != nil {
		t.Fatal(err)
	}
	return base64.RawURLEncoding.EncodeToString(cdJSON),
		base64.RawURLEncoding.EncodeToString(attObj),
		base64.RawURLEncoding.EncodeToString(idBytes)
}

func TestLibCreationHappyPath(t *testing.T) {
	setupWA(t)
	priv, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	wantChal := base64.RawURLEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef"))
	cdB64, attB64, credID := buildCreationSelfAttested(t, priv, wantChal, testRPID, testOrigin, 0x01|0x40|0x04)
	got, err := parseCreationLib(cdB64, attB64, wantChal, credID)
	if err != nil {
		t.Fatalf("creation happy path failed: %v", err)
	}
	if got.CredentialID != credID || len(got.PublicKey) == 0 {
		t.Fatal("empty creation fields")
	}
	if got.AttFormat != "packed" {
		t.Fatalf("att format = %q, want packed", got.AttFormat)
	}
}

func TestLibCreationRecordsBackupFlags(t *testing.T) {
	setupWA(t)
	priv, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	wantChal := base64.RawURLEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef"))
	// Synced-provider shape: BE|BS set. These flags must survive into
	// the stored credential or every later login fails closed.
	cdB64, attB64, credID := buildCreationSelfAttested(t, priv, wantChal, testRPID, testOrigin, 0x01|0x40|0x04|0x08|0x10)
	got, err := parseCreationLib(cdB64, attB64, wantChal, credID)
	if err != nil {
		t.Fatalf("BE/BS creation failed: %v", err)
	}
	if !got.BackupEligible || !got.BackupState {
		t.Fatalf("backup flags not recorded: %+v", got)
	}
	// And a login against the recorded flags must pass (the production
	// failure was zeroed stored flags vs BE-presented assertion).
	ad, cd, sig := loginArgs(t, priv, flagUPUVBEBS, 1, testNonce, testOrigin)
	stored := &WAStoredCred{
		CredentialID:   credID,
		PublicKey:      base64.RawURLEncoding.EncodeToString(coseEC2(t, &priv.PublicKey)),
		BackupEligible: got.BackupEligible,
		BackupState:    got.BackupState,
	}
	if _, err := verifyAssertionLib(stored, testNonce, ad, cd, sig, testRole); err != nil {
		t.Fatalf("login with recorded flags failed: %v", err)
	}
}

func TestLibCreationRejections(t *testing.T) {
	setupWA(t)
	priv, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	wantChal := base64.RawURLEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef"))
	cdB64, attB64, credID := buildCreationSelfAttested(t, priv, wantChal, testRPID, testOrigin, 0x01|0x40|0x04)

	if _, err := parseCreationLib(cdB64, attB64,
		base64.RawURLEncoding.EncodeToString([]byte("other-challenge-32-bytes!!!!!!")), credID); err == nil {
		t.Error("wrong challenge accepted")
	}
	if _, err := parseCreationLib(cdB64, attB64, wantChal, "bm90LWV4aXN0aW5n"); err == nil {
		t.Error("claimed ID mismatch accepted")
	}
	noUVcd, noUVatt, noUVid := buildCreationSelfAttested(t, priv, wantChal, testRPID, testOrigin, 0x01|0x40)
	if _, err := parseCreationLib(noUVcd, noUVatt, wantChal, noUVid); err == nil {
		t.Error("missing UV accepted")
	}
	if _, err := parseCreationLib("!!!", attB64, wantChal, credID); err == nil {
		t.Error("malformed clientData accepted")
	}
	if _, err := parseCreationLib(cdB64, "AA", wantChal, credID); err == nil {
		t.Error("1-byte attestation accepted")
	}
}

func TestChallengeFromNonceDeterministic(t *testing.T) {
	a := ChallengeFromNonce(testNonce)
	b := ChallengeFromNonce(testNonce)
	if string(a) != string(b) || len(a) != 32 {
		t.Fatal("challenge derivation not deterministic 32-byte")
	}
}

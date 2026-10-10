package main

import (
	"errors"
	"strings"
	"testing"
)

// TestVerifyAssertionKeepsTheLibraryReason: the library knows why an
// assertion was rejected, and the operator is the only one who can act on it.
// Wrapping the sentinel without its error leaves an unresolvable log line.
func TestVerifyAssertionKeepsTheLibraryReason(t *testing.T) {
	s := pairServer(t)
	store := NewWebAuthnStore(t.TempDir() + "/webauthn.json")
	s.WebAuthn = store
	auth := newTestPasskey(t, store, "member")

	// A well-formed assertion over the wrong challenge: the signature is
	// real, so this fails inside the library rather than at parsing.
	challenge := strings.Repeat("ab", 64)
	doc := auth.assert(t, challenge)
	cred, ok := store.Credential("member", auth.credentialID())
	if !ok {
		t.Fatal("the enrolled credential is missing")
	}

	_, err := verifyAssertionLib(cred, challenge+"ff", doc["auth_data"].(string),
		doc["client_data"].(string), doc["sig"].(string), "member")
	if err == nil {
		t.Fatal("a mismatched challenge verified")
	}
	if !errors.Is(err, errPasskey) {
		t.Fatalf("err = %v, want it to stay classified as errPasskey", err)
	}
	// The bare form names the step and nothing else, which is the state
	// this guards against.
	if err.Error() == errPasskey.Error()+": login_rejected" {
		t.Fatalf("the library's reason was discarded: %q", err)
	}
}

// TestParseCreationKeepsTheLibraryReason: same for the enrollment ceremony,
// where a rejected registration is equally opaque to the operator.
func TestParseCreationKeepsTheLibraryReason(t *testing.T) {
	s := pairServer(t)
	store := NewWebAuthnStore(t.TempDir() + "/webauthn.json")
	s.WebAuthn = store
	auth := newTestPasskey(t, store, "member")

	// A real creation document is not available here, so a malformed one
	// proves the point: the error must carry what the library said.
	_, err := parseCreationLib("bad", "bad", "challenge", auth.credentialID())
	if err == nil {
		t.Fatal("garbage registration material parsed")
	}
	if !errors.Is(err, errPasskey) {
		t.Fatalf("err = %v, want it to stay classified as errPasskey", err)
	}
	if err.Error() == errPasskey.Error()+": creation_malformed" {
		t.Fatalf("the library's reason was discarded: %q", err)
	}
}

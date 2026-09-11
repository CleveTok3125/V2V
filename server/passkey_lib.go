package main

// WebAuthn verification through the go-webauthn library: the adapter
// translates the repo's compact wire fields into standard ceremony
// documents and lets the library enforce challenge, origin, RP ID,
// flags, signature, counter and credential policy. Hand-rolled
// verification was deleted; this file plus the library own the whole
// ceremony surface now.
//
// Hard policy lives here, not in callers: user verification required,
// ES256 only, attestation "none" rejected, credential IDs clamped to
// 1023 bytes.

import (
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/protocol/webauthncose"
	"github.com/go-webauthn/webauthn/webauthn"
)

const maxCredentialIDLen = 1023

// waUser implements webauthn.User over a single stored credential.
// UserID is synthetic but stable per (role, credential): the library
// requires it to equal the session's, and binding both to the same
// value keeps cross-credential confusion impossible.
type waUser struct {
	id   []byte
	name string
	cred webauthn.Credential
}

func (u waUser) WebAuthnID() []byte                        { return u.id }
func (u waUser) WebAuthnName() string                      { return u.name }
func (u waUser) WebAuthnDisplayName() string               { return u.name }
func (u waUser) WebAuthnCredentials() []webauthn.Credential { return []webauthn.Credential{u.cred} }

// libSession builds the session both ceremonies share. Challenge is
// the base64url the client signed; user verification is required;
// expiry is a backstop (server nonce/ticket TTLs own the real window).
func libSession(challengeB64 string, uid []byte) webauthn.SessionData {
	return webauthn.SessionData{
		Challenge:        challengeB64,
		UserID:           uid,
		UserVerification: protocol.VerificationRequired,
		Expires:          time.Now().Add(5 * time.Minute),
	}
}

// es256Only restricts creation to ES256; the library rejects anything
// else before a credential exists.
func es256Only() []protocol.CredentialParameter {
	return []protocol.CredentialParameter{
		{Type: protocol.PublicKeyCredentialType, Algorithm: webauthncose.AlgES256},
	}
}

// verifyAssertionLib validates one login assertion through the library
// and returns the authenticator counter for clone detection.
func verifyAssertionLib(pubKeyCOSE []byte, nonceHex, authDataB64, clientDataB64, sigB64, credIDB64, role string) (uint32, error) {
	if WebAuth == nil {
		return 0, perr("passkey_disabled")
	}
	rawID, err := base64.RawURLEncoding.DecodeString(credIDB64)
	if err != nil || len(rawID) == 0 || len(rawID) > maxCredentialIDLen {
		return 0, perr("credential_id_malformed")
	}
	doc := fmt.Sprintf(`{"id":%q,"rawId":%q,"type":"public-key","response":{"authenticatorData":%q,"clientDataJSON":%q,"signature":%q}}`,
		credIDB64, credIDB64, authDataB64, clientDataB64, sigB64)
	parsed, err := protocol.ParseCredentialRequestResponseBytes([]byte(doc))
	if err != nil {
		return 0, perr("assertion_malformed")
	}
	uidSum := sha256.Sum256([]byte(role + "\x00" + credIDB64))
	chalSum := ChallengeFromNonce(nonceHex)
	session := libSession(base64.RawURLEncoding.EncodeToString(chalSum), uidSum[:])
	session.AllowedCredentialIDs = [][]byte{rawID}
	user := waUser{id: uidSum[:], name: role, cred: webauthn.Credential{ID: rawID, PublicKey: pubKeyCOSE}}
	cred, err := WebAuth.ValidateLogin(user, session, parsed)
	if err != nil {
		return 0, fmt.Errorf("%w: login_rejected", errPasskey)
	}
	if !parsed.Response.AuthenticatorData.Flags.UserVerified() {
		return 0, perr("user_verification_required")
	}
	return cred.Authenticator.SignCount, nil
}

// LibCreation is a library-validated registration result.
type LibCreation struct {
	CredentialID string
	PublicKey    []byte // COSE_Key CBOR
	Counter      uint32
	AttFormat    string
}

// parseCreationLib validates a registration ceremony through the
// library and enforces the repo's creation policy.
func parseCreationLib(clientDataB64, attObjB64, wantChallengeB64, claimedID string) (*LibCreation, error) {
	if WebAuth == nil {
		return nil, perr("passkey_disabled")
	}
	doc := fmt.Sprintf(`{"id":%q,"rawId":%q,"type":"public-key","response":{"clientDataJSON":%q,"attestationObject":%q}}`,
		claimedID, claimedID, clientDataB64, attObjB64)
	parsed, err := protocol.ParseCredentialCreationResponseBytes([]byte(doc))
	if err != nil {
		return nil, perr("creation_malformed")
	}
	uidSum := sha256.Sum256([]byte("enroll\x00" + wantChallengeB64))
	session := libSession(wantChallengeB64, uidSum[:])
	session.CredParams = es256Only()
	user := waUser{id: uidSum[:], name: "enroll"}
	cred, err := WebAuth.CreateCredential(user, session, parsed)
	if err != nil {
		return nil, fmt.Errorf("%w: registration_rejected", errPasskey)
	}
	gotID := base64.RawURLEncoding.EncodeToString(cred.ID)
	if gotID != claimedID {
		return nil, perr("credential_id_mismatch")
	}
	if len(cred.ID) == 0 || len(cred.ID) > maxCredentialIDLen {
		return nil, perr("credential_id_out_of_range")
	}
	if string(cred.AttestationFormat) == "none" || cred.AttestationFormat == "" {
		return nil, perr("attestation_required")
	}
	if !parsed.Response.AttestationObject.AuthData.Flags.UserVerified() {
		return nil, perr("user_verification_required")
	}
	return &LibCreation{
		CredentialID: gotID,
		PublicKey:    cred.PublicKey,
		Counter:      cred.Authenticator.SignCount,
		AttFormat:    string(cred.AttestationFormat),
	}, nil
}

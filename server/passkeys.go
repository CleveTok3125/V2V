package main

// Passkey (WebAuthn) support. Passkeys are just another identity type inside
// roles.json: enrollment happens entirely client-side on a static page, and
// only public material (credential ID + COSE public key) is ever handed to
// the admin for import — the exact trust model of ed25519 key files.
//
// Login-time verification mirrors the key-file flow: the server's auth nonce
// doubles as the WebAuthn challenge (SHA-256 of the hex nonce), and the
// assertion is checked against the stored COSE public key.

import (
	"crypto/sha256"
	"errors"
	"fmt"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"

	"github.com/CleveTok3125/V2V/internal/env"
)

type webauthnConfig struct {
	RPID    string // e.g. "example.com"
	Origin  string // e.g. "https://chat.example.com"
	Enabled bool
}

var WAConfig webauthnConfig

// WebAuth is the global WebAuthn instance. Verification runs 100%
// through the library (attestation direct, resident key + user
// verification required); the manual verifier was deleted.
var WebAuth *webauthn.WebAuthn

func LoadWebauthnEnv() {
	WAConfig.RPID = env.WebauthnRPID()
	WAConfig.Origin = env.WebauthnOrigin()
	WAConfig.Enabled = WAConfig.RPID != "" && WAConfig.Origin != ""
	if !WAConfig.Enabled {
		fmt.Println("ℹ️ WEBAUTHN_RPID/WEBAUTHN_ORIGIN chưa đặt — đăng nhập bằng passkey đang TẮT")
		return
	}
	var err error
	WebAuth, err = webauthn.New(&webauthn.Config{
		RPDisplayName: "V2V",
		RPID:          WAConfig.RPID,
		RPOrigins:     []string{WAConfig.Origin},
		AttestationPreference: protocol.PreferDirectAttestation,
		AuthenticatorSelection: protocol.AuthenticatorSelection{
			RequireResidentKey: protocol.ResidentKeyRequired(),
			ResidentKey:        protocol.ResidentKeyRequirementRequired,
			UserVerification:   protocol.VerificationRequired,
		},
	})
	if err != nil {
		fmt.Printf("❌ WebAuthn init failed: %v\n", err)
		WAConfig.Enabled = false
	}
}

// ChallengeFromNonce is the single source of truth mapping an auth nonce to
// the WebAuthn challenge bytes both the client and this verifier derive.
func ChallengeFromNonce(nonceHex string) []byte {
	h := sha256.Sum256([]byte(nonceHex))
	return h[:]
}

var errPasskey = errors.New("passkey_error")

func perr(reason string) error { return fmt.Errorf("%w: %s", errPasskey, reason) }

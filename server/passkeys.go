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
	"bytes"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"os"

	"github.com/fxamacker/cbor/v2"
)

const (
	coseAlgES256 = -7
	coseAlgRS256 = -257

	authDataFlagUP = 0x01 // user present
)

type webauthnConfig struct {
	RPID    string // e.g. "example.com"
	Origin  string // e.g. "https://chat.example.com"
	Enabled bool
}

var WAConfig webauthnConfig

func LoadWebauthnEnv() {
	WAConfig.RPID = os.Getenv("WEBAUTHN_RPID")
	WAConfig.Origin = os.Getenv("WEBAUTHN_ORIGIN")
	WAConfig.Enabled = WAConfig.RPID != "" && WAConfig.Origin != ""
	if !WAConfig.Enabled {
		fmt.Println("ℹ️ WEBAUTHN_RPID/WEBAUTHN_ORIGIN chưa đặt — đăng nhập bằng passkey đang TẮT")
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

// clientData is the subset of CollectedClientData we validate.
type clientData struct {
	Type      string `json:"type"`
	Challenge string `json:"challenge"`
	Origin    string `json:"origin"`
}

// verifyAssertion validates one login assertion against a stored credential:
// challenge binding, origin, rpIdHash, user-present flag and the signature.
func verifyAssertion(pubKeyCOSE []byte, nonceHex string, authDataB64, clientDataB64, sigB64 string) (uint32, error) {
	rawAuth, err := base64.RawURLEncoding.DecodeString(authDataB64)
	if err != nil || len(rawAuth) < 37 {
		return 0, perr("authenticator_data_malformed")
	}
	rawCD, err := base64.RawURLEncoding.DecodeString(clientDataB64)
	if err != nil {
		return 0, perr("client_data_malformed")
	}
	sig, err := base64.RawURLEncoding.DecodeString(sigB64)
	if err != nil {
		return 0, perr("signature_malformed")
	}
	counter := uint32(rawAuth[33])<<24 | uint32(rawAuth[34])<<16 | uint32(rawAuth[35])<<8 | uint32(rawAuth[36])

	var cd clientData
	if err := json.Unmarshal(rawCD, &cd); err != nil {
		return 0, perr("client_data_invalid_json")
	}
	if cd.Type != "webauthn.get" {
		return 0, perr("client_data_wrong_type")
	}
	wantChallenge := ChallengeFromNonce(nonceHex)
	gotChallenge, err := base64.RawURLEncoding.DecodeString(cd.Challenge)
	if err != nil || !bytes.Equal(gotChallenge, wantChallenge) {
		return counter, perr("challenge_mismatch")
	}
	if subtle.ConstantTimeCompare([]byte(cd.Origin), []byte(WAConfig.Origin)) != 1 {
		return counter, perr("origin_mismatch")
	}

	rpIDHash := sha256.Sum256([]byte(WAConfig.RPID))
	if !bytes.Equal(rawAuth[:32], rpIDHash[:]) {
		return counter, perr("rp_id_hash_mismatch")
	}
	if rawAuth[32]&authDataFlagUP == 0 {
		return counter, perr("user_not_present")
	}

	cdHash := sha256.Sum256(rawCD)
	signed := append(append([]byte{}, rawAuth...), cdHash[:]...)
	digest := sha256.Sum256(signed)

	pub, alg, err := parseCOSEKey(pubKeyCOSE)
	if err != nil {
		return counter, err
	}
	switch k := pub.(type) {
	case *ecdsa.PublicKey:
		if alg != coseAlgES256 || !ecdsa.VerifyASN1(k, digest[:], sig) {
			return counter, perr("signature_invalid")
		}
	case *rsa.PublicKey:
		if alg != coseAlgRS256 || rsa.VerifyPKCS1v15(k, crypto.SHA256, digest[:], sig) != nil {
			return counter, perr("signature_invalid")
		}
	default:
		return counter, perr("unsupported_key_type")
	}
	return counter, nil
}

// ParsedCreation carries the pieces the server must persist from a
// registration ceremony.
type ParsedCreation struct {
	CredentialID string // base64url
	PublicKey    []byte // COSE_Key CBOR
	Counter      uint32
}

// parseCreationForImport validates a browser-side registration ceremony and
// extracts the credential to import: clientDataJSON type/challenge/origin,
// rpIdHash, user-present + attested-credential-data flags, then splits out
// credential ID and COSE public key from the attested data section.
// Attestation statements are ignored (attestation:"none").
func parseCreationForImport(clientDataB64, attObjB64, wantChallengeB64 string) (*ParsedCreation, error) {
	rawCD, err := base64.RawURLEncoding.DecodeString(clientDataB64)
	if err != nil {
		return nil, perr("client_data_malformed")
	}
	var cd clientData
	if err := json.Unmarshal(rawCD, &cd); err != nil {
		return nil, perr("client_data_invalid_json")
	}
	if cd.Type != "webauthn.create" {
		return nil, perr("client_data_wrong_type")
	}
	gotChal, err := base64.RawURLEncoding.DecodeString(cd.Challenge)
	wantChal, err2 := base64.RawURLEncoding.DecodeString(wantChallengeB64)
	if err != nil || err2 != nil || !bytes.Equal(gotChal, wantChal) {
		fmt.Printf("🔍 [PARSE FAIL] challenge mismatch: got=%s want=%s\n", cd.Challenge[:12], wantChallengeB64[:12])
		return nil, perr("challenge_mismatch")
	}
	if subtle.ConstantTimeCompare([]byte(cd.Origin), []byte(WAConfig.Origin)) != 1 {
		fmt.Printf("🔍 [PARSE FAIL] origin mismatch: got=%q want=%q\n", cd.Origin, WAConfig.Origin)
		return nil, perr("origin_mismatch")
	}

	rawAtt, err := base64.RawURLEncoding.DecodeString(attObjB64)
	if err != nil {
		return nil, perr("attestation_malformed")
	}
	var obj map[int]any
	if err := cbor.Unmarshal(rawAtt, &obj); err != nil {
		return nil, perr("attestation_invalid_cbor")
	}
	authData, _ := obj[2].([]byte)
	if len(authData) < 55 { // 32 rpId + 1 flags + 4 counter + 16 aaguid + 2 len
		return nil, perr("authenticator_data_malformed")
	}
	rpIDHash := sha256.Sum256([]byte(WAConfig.RPID))
	if !bytes.Equal(authData[:32], rpIDHash[:]) {
		fmt.Printf("🔍 [PARSE FAIL] rpIdHash mismatch for RPID=%q\n", WAConfig.RPID)
		return nil, perr("rp_id_hash_mismatch")
	}
	const flagUP = 0x01
	const flagAT = 0x40
	if authData[32]&flagUP == 0 {
		fmt.Printf("🔍 [PARSE FAIL] user_present flag not set (flags=0x%02x)\n", authData[32])
		return nil, perr("user_not_present")
	}
	if authData[32]&flagAT == 0 {
		fmt.Printf("🔍 [PARSE FAIL] attested_data flag not set (flags=0x%02x)\n", authData[32])
		return nil, perr("attested_data_missing")
	}
	counter := uint32(authData[33])<<24 | uint32(authData[34])<<16 | uint32(authData[35])<<8 | uint32(authData[36])
	idLen := int(authData[53])<<8 | int(authData[54])
	if 55+idLen > len(authData) {
		return nil, perr("credential_id_out_of_range")
	}
	credID := authData[55 : 55+idLen]
	cose := authData[55+idLen:]

	pub, alg, err := parseCOSEKey(cose)
	if err != nil {
		return nil, err
	}
	ok := false
	switch k := pub.(type) {
	case *ecdsa.PublicKey:
		ok = alg == coseAlgES256 && k.Curve == elliptic.P256()
	case *rsa.PublicKey:
		ok = alg == coseAlgRS256
	}
	if !ok {
		return nil, perr("cose_alg_unsupported")
	}
	return &ParsedCreation{
		CredentialID: base64.RawURLEncoding.EncodeToString(credID),
		PublicKey:    cose,
		Counter:      counter,
	}, nil
}

// parseCOSEKey decodes a COSE_Key CBOR blob into a usable public key.
// Supported algorithms: ES256 (-7, P-256) and RS256 (-257).
func parseCOSEKey(blob []byte) (any, int, error) {
	var m map[int]any
	if err := cbor.Unmarshal(blob, &m); err != nil {
		return nil, 0, perr("cose_key_invalid_cbor")
	}
	alg, okA := coseInt(m[3])
	kty, okK := coseInt(m[1])
	crv, _ := coseInt(m[-1])
	switch {
	case okK && okA && kty == 2 && alg == coseAlgES256 && crv == 1:
		x, okX := m[-2].([]byte)
		y, okY := m[-3].([]byte)
		if !okX || !okY {
			return nil, 0, perr("cose_key_bad_ec2")
		}
		pk := &ecdsa.PublicKey{Curve: elliptic.P256(), X: new(big.Int).SetBytes(x), Y: new(big.Int).SetBytes(y)}
		if !pk.Curve.IsOnCurve(pk.X, pk.Y) {
			return nil, 0, perr("cose_key_point_off_curve")
		}
		return pk, coseAlgES256, nil
	case okK && okA && kty == 3 && alg == coseAlgRS256:
		n, okN := m[-1].([]byte)
		e, okE := m[-2].([]byte)
		if !okN || !okE || len(e) == 0 || len(e) > 8 {
			return nil, 0, perr("cose_key_bad_rsa")
		}
		eInt := new(big.Int).SetBytes(e)
		if !eInt.IsInt64() {
			return nil, 0, perr("cose_key_bad_rsa")
		}
		return &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: int(eInt.Int64())}, coseAlgRS256, nil
	default:
		return nil, 0, perr("cose_alg_unsupported")
	}
}

// coseInt normalizes CBOR-decoded integers: unsigned values arrive as
// uint64, negatives as int64.
func coseInt(v any) (int64, bool) {
	switch x := v.(type) {
	case int64:
		return x, true
	case uint64:
		if x > ^uint64(0)>>1 {
			return 0, false
		}
		return int64(x), true
	case int:
		return int64(x), true
	default:
		return 0, false
	}
}

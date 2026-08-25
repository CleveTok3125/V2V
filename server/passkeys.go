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
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"os"
	"time"

	"github.com/fxamacker/cbor/v2"
)

const (
	coseAlgES256 = -7
	coseAlgRS256 = -257

	authDataFlagUP = 0x01 // user present
)

type webauthnConfig struct {
	RPID    string // e.g. "elsutm.io.vn"
	Origin  string // e.g. "https://chat.elsutm.io.vn"
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

// NewPairNonce allocates a device-pairable nonce: same lifecycle as a normal
// auth nonce, but its assertion may arrive from another device/IP.
func (s *ChatServer) NewPairNonce(ttl time.Duration) (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	nonce := fmt.Sprintf("%x", b)
	s.ActiveNonces.Store(nonce, NonceMeta{
		ExpiresAt: time.Now().Add(ttl),
		PairMode:  true,
	})
	return nonce, nil
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
func verifyAssertion(pubKeyCOSE []byte, nonceHex string, authDataB64, clientDataB64, sigB64 string) error {
	rawAuth, err := base64.RawURLEncoding.DecodeString(authDataB64)
	if err != nil || len(rawAuth) < 37 {
		return perr("authenticator_data_malformed")
	}
	rawCD, err := base64.RawURLEncoding.DecodeString(clientDataB64)
	if err != nil {
		return perr("client_data_malformed")
	}
	sig, err := base64.RawURLEncoding.DecodeString(sigB64)
	if err != nil {
		return perr("signature_malformed")
	}

	var cd clientData
	if err := json.Unmarshal(rawCD, &cd); err != nil {
		return perr("client_data_invalid_json")
	}
	if cd.Type != "webauthn.get" {
		return perr("client_data_wrong_type")
	}
	wantChallenge := ChallengeFromNonce(nonceHex)
	gotChallenge, err := base64.RawURLEncoding.DecodeString(cd.Challenge)
	if err != nil || !bytes.Equal(gotChallenge, wantChallenge) {
		return perr("challenge_mismatch")
	}
	if subtle.ConstantTimeCompare([]byte(cd.Origin), []byte(WAConfig.Origin)) != 1 {
		return perr("origin_mismatch")
	}

	rpIDHash := sha256.Sum256([]byte(WAConfig.RPID))
	if !bytes.Equal(rawAuth[:32], rpIDHash[:]) {
		return perr("rp_id_hash_mismatch")
	}
	if rawAuth[32]&authDataFlagUP == 0 {
		return perr("user_not_present")
	}

	cdHash := sha256.Sum256(rawCD)
	signed := append(append([]byte{}, rawAuth...), cdHash[:]...)
	digest := sha256.Sum256(signed)

	pub, alg, err := parseCOSEKey(pubKeyCOSE)
	if err != nil {
		return err
	}
	switch k := pub.(type) {
	case *ecdsa.PublicKey:
		if alg != coseAlgES256 || !ecdsa.VerifyASN1(k, digest[:], sig) {
			return perr("signature_invalid")
		}
	case *rsa.PublicKey:
		if alg != coseAlgRS256 || rsa.VerifyPKCS1v15(k, crypto.SHA256, digest[:], sig) != nil {
			return perr("signature_invalid")
		}
	default:
		return perr("unsupported_key_type")
	}
	return nil
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

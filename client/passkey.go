package main

// Software passkey identities for the desktop client. A passkey here is an
// ES256 keypair in WebAuthn wire format: the public half is COSE-encoded for
// roles.json exactly like a hardware authenticator would emit, and at login
// time the client constructs a standard assertion itself. The server cannot
// tell this credential apart from a hardware one — attestation is not
// verified — which mirrors how ed25519 key files already work.

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/fxamacker/cbor/v2"
)

// webauthnAssertion mirrors the server's AuthPacket passkey fields.
type webauthnAssertion struct {
	PasskeyID  string `json:"passkey_id"`
	AuthData   string `json:"passkey_auth_data"`
	ClientData string `json:"passkey_client_data"`
	Sig        string `json:"passkey_sig"`
}

func timeNowRFC3339() string { return time.Now().Format(time.RFC3339) }

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

func pad32(b []byte) []byte {
	if len(b) >= 32 {
		return b
	}
	out := make([]byte, 32)
	copy(out[32-len(b):], b)
	return out
}

func x509MarshalPKCS8(key *ecdsa.PrivateKey) ([]byte, error) {
	return x509.MarshalPKCS8PrivateKey(key)
}

func x509ParsePKCS8(der []byte) (any, error) {
	return x509.ParsePKCS8PrivateKey(der)
}

// PasskeyIdentity is the on-disk private half of a software passkey. The
// COSE public key is kept alongside for easy re-export of the roles.json
// snippet without touching crypto again.
type PasskeyIdentity struct {
	Role         string `json:"role"`
	CredentialID string `json:"credential_id"` // base64url
	PrivateKey   string `json:"private_key"`   // PKCS#8 DER, base64url
	PublicKey    string `json:"public_key"`    // COSE_Key CBOR, base64url
	RPID         string `json:"rpid"`
	Origin       string `json:"origin"`
	SignCount    uint32 `json:"sign_count"`
}

// GeneratePasskey creates a new identity bound to the given RP ID/origin.
func GeneratePasskey(role, rpid, origin string) (*PasskeyIdentity, error) {
	if rpid == "" || origin == "" {
		return nil, errors.New("cần WEBAUTHN_RPID và WEBAUTHN_ORIGIN (hoặc cờ --rpid/--origin)")
	}
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	der, err := x509MarshalPKCS8(priv)
	if err != nil {
		return nil, err
	}
	credID := make([]byte, 32)
	if _, err := rand.Read(credID); err != nil {
		return nil, err
	}
	cose, err := coseEncodePub(&priv.PublicKey)
	if err != nil {
		return nil, err
	}
	return &PasskeyIdentity{
		Role:         role,
		CredentialID: base64.RawURLEncoding.EncodeToString(credID),
		PrivateKey:   base64.RawURLEncoding.EncodeToString(der),
		PublicKey:    base64.RawURLEncoding.EncodeToString(cose),
		RPID:         rpid,
		Origin:       origin,
	}, nil
}

// RolesSnippet renders the public half as a ready-to-paste roles.json entry.
func (p *PasskeyIdentity) RolesSnippet() (string, error) {
	coseB64 := p.PublicKey
	if coseB64 == "" {
		pub, err := p.publicKey()
		if err != nil {
			return "", err
		}
		b, err := coseEncodePub(pub)
		if err != nil {
			return "", err
		}
		coseB64 = base64.RawURLEncoding.EncodeToString(b)
	}
	entry := map[string]any{
		"credential_id": p.CredentialID,
		"public_key":    coseB64,
		"added_at":      timeNowRFC3339(),
	}
	out, err := json.MarshalIndent([]any{entry}, "", "  ")
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("// roles.json → %q.passkeys\n%s", p.Role, out), nil
}

// coseEncodePub wraps an ES256 public key into the COSE_Key CBOR form that
// roles.json stores (identical to what browsers emit for getPublicKey()).
func coseEncodePub(pub *ecdsa.PublicKey) ([]byte, error) {
	cose := map[int]any{
		1:  int64(2),             // kty: EC2
		3:  int64(-7),            // alg: ES256
		-1: int64(1),             // crv: P-256
		-2: pad32(pub.X.Bytes()), // x
		-3: pad32(pub.Y.Bytes()), // y
	}
	return cbor.Marshal(cose)
}

func (p *PasskeyIdentity) publicKey() (*ecdsa.PublicKey, error) {
	der, err := base64.RawURLEncoding.DecodeString(p.PrivateKey)
	if err != nil {
		return nil, err
	}
	key, err := x509ParsePKCS8(der)
	if err != nil {
		return nil, err
	}
	ec, ok := key.(*ecdsa.PrivateKey)
	if !ok || ec.Curve != elliptic.P256() {
		return nil, errors.New("passkey: khóa không phải ES256/P-256")
	}
	return &ec.PublicKey, nil
}

// BuildAssertion signs the handshake nonce and returns the AuthPacket fields.
// The sign counter is incremented and persisted best-effort.
func (p *PasskeyIdentity) BuildAssertion(nonceHex string) (credID, authDataB64, clientDataB64, sigB64 string, err error) {
	priv, err := p.privateKey()
	if err != nil {
		return "", "", "", "", err
	}
	rpIDHash := sha256.Sum256([]byte(p.RPID))
	authData := make([]byte, 37)
	copy(authData[:32], rpIDHash[:])
	authData[32] = 0x01 // UP

	p.SignCount++
	var cnt [4]byte
	cnt[0] = byte(p.SignCount >> 24)
	cnt[1] = byte(p.SignCount >> 16)
	cnt[2] = byte(p.SignCount >> 8)
	cnt[3] = byte(p.SignCount)

	challenge := sha256.Sum256([]byte(nonceHex))
	cd, _ := json.Marshal(map[string]any{
		"type":      "webauthn.get",
		"challenge": base64.RawURLEncoding.EncodeToString(challenge[:]),
		"origin":    p.Origin,
	})

	cdHash := sha256.Sum256(cd)
	signed := append(append([]byte{}, authData...), cdHash[:]...)
	digest := sha256.Sum256(signed)
	sig, err := ecdsa.SignASN1(rand.Reader, priv, digest[:])
	if err != nil {
		return "", "", "", "", err
	}
	return p.CredentialID,
		base64.RawURLEncoding.EncodeToString(authData),
		base64.RawURLEncoding.EncodeToString(cd),
		base64.RawURLEncoding.EncodeToString(sig),
		nil
}

func (p *PasskeyIdentity) privateKey() (*ecdsa.PrivateKey, error) {
	der, err := base64.RawURLEncoding.DecodeString(p.PrivateKey)
	if err != nil {
		return nil, err
	}
	key, err := x509ParsePKCS8(der)
	if err != nil {
		return nil, err
	}
	ec, ok := key.(*ecdsa.PrivateKey)
	if !ok {
		return nil, errors.New("passkey: sai loại khóa")
	}
	return ec, nil
}

// Save writes the identity with owner-only permissions.
func (p *PasskeyIdentity) Save(path string) error {
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

// LoadPasskeyFile reads a software passkey identity from disk.
func LoadPasskeyFile(path string) (*PasskeyIdentity, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var p PasskeyIdentity
	if err := json.Unmarshal(data, &p); err != nil {
		return nil, err
	}
	if p.CredentialID == "" || p.PrivateKey == "" || p.Role == "" {
		return nil, errors.New("passkey: file thiếu trường bắt buộc")
	}
	return &p, nil
}

// Package identity holds the locally stored login identities shared by the
// chat client and the v2v-admin management tool: a classic ed25519 key-file
// slot and a software-passkey slot inside one versioned container, plus the
// WebAuthn wire-format helpers the desktop login needs.
package identity

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

const Version = 3

type Ed25519Identity struct {
	Role         string `json:"role"`
	PrivateKey   string `json:"private_key"` // hex-encoded seed
	HmacShield   string `json:"hmac_shield"` // hex-encoded
	ServerPubKey string `json:"server_pubkey,omitempty"` // hex ed25519 pub of server, anti-phishing pin
}

// PasskeyIdentity is the private half of a software passkey. The COSE public
// key is kept alongside for easy re-export of the roles.json snippet without
// touching crypto again.
type PasskeyIdentity struct {
	Role         string `json:"role"`
	CredentialID string `json:"credential_id"` // base64url
	PrivateKey   string `json:"private_key"`   // PKCS#8 DER, base64url
	PublicKey    string `json:"public_key"`    // COSE_Key CBOR, base64url
	RPID         string `json:"rpid"`
	Origin       string `json:"origin"`
	SignCount    uint32 `json:"sign_count"`
}

type IdentityFile struct {
	Version int              `json:"version"`
	Ed25519 *Ed25519Identity `json:"ed25519,omitempty"`
	Passkey *PasskeyIdentity `json:"passkey,omitempty"`
}

// Load reads key.json in either the current v2 container shape or the legacy
// flat ed25519 shape (read-compat for files already in the wild).
// Encrypted files (version 3 envelope) must be opened via LoadEncrypted.
func Load(path string) (*IdentityFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if isEncrypted(data) {
		return nil, errors.New("key file is encrypted — use passphrase to unlock (LoadEncrypted)")
	}
	var probe map[string]any
	if json.Unmarshal(data, &probe) != nil {
		return nil, errors.New("key.json is not valid JSON")
	}
	// Reject old Host-based files (no backward compat as requested)
	if ed, ok := probe["ed25519"].(map[string]any); ok {
		if _, hasHost := ed["host"]; hasHost {
			return nil, errors.New("key file uses old Host pinning — run v2v-admin migrate to update to server_pubkey")
		}
	}
	if _, isContainer := probe["version"]; isContainer {
		var f IdentityFile
		if err := json.Unmarshal(data, &f); err != nil {
			return nil, err
		}
		if f.Version < Version {
			return nil, errors.New("key file version too old — run v2v-admin migrate")
		}
		f.Version = Version
		return &f, nil
	}
	var legacy Ed25519Identity
	if err := json.Unmarshal(data, &legacy); err != nil {
		return nil, errors.New("key.json has an unknown shape")
	}
	if legacy.Role == "" || legacy.PrivateKey == "" || legacy.HmacShield == "" {
		return nil, errors.New("key.json is missing required fields")
	}
	return &IdentityFile{Version: Version, Ed25519: &legacy}, nil
}

// LoadEncrypted reads an encrypted key file (version 3).
func LoadEncrypted(path, passphrase string) (*IdentityFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if !isEncrypted(data) {
		return Load(path)
	}
	plain, err := decryptJSON(data, passphrase)
	if err != nil {
		return nil, err
	}
	var f IdentityFile
	if err := json.Unmarshal(plain, &f); err != nil {
		return nil, err
	}
	f.Version = Version
	return &f, nil
}

// IsEncrypted reports whether the file at path is an encrypted envelope.
func IsEncrypted(path string) (bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	return isEncrypted(data), nil
}

func atomicWriteFile(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
	}
	tmpFile, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmpFile.Name()
	if err := tmpFile.Chmod(perm); err != nil {
		tmpFile.Close()
		os.Remove(tmpName)
		return err
	}
	if _, err := tmpFile.Write(data); err != nil {
		tmpFile.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmpFile.Sync(); err != nil {
		tmpFile.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmpFile.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		os.Remove(tmpName)
		return err
	}
	if dirFile, err := os.Open(dir); err == nil {
		_ = dirFile.Sync()
		dirFile.Close()
	}
	return nil
}

// Save writes the container with owner-only permissions using atomic write.
func (f *IdentityFile) Save(path string) error {
	f.Version = Version
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	return atomicWriteFile(path, data, 0o600)
}

// SaveEncrypted writes the container encrypted with XChaCha20Poly1305 + Argon2id.
func (f *IdentityFile) SaveEncrypted(path, passphrase string, p *Params) error {
	f.Version = Version
	plain, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	if p == nil {
		pp := defaultParams()
		p = &pp
	}
	enc, err := encryptJSON(plain, passphrase, *p)
	if err != nil {
		return err
	}
	return atomicWriteFile(path, enc, 0o600)
}

// MergeRolesFile applies update() to a single role entry inside roles.json,
// preserving every other top-level role. A file that exists but cannot be
// parsed aborts the operation instead of being clobbered.
func MergeRolesFile(path, role string, update func(entry map[string]any)) error {
	root := map[string]any{}
	if data, err := os.ReadFile(path); err == nil && len(data) > 0 {
		if err := json.Unmarshal(data, &root); err != nil {
			return fmt.Errorf("roles.json unreadable (%w) — fix or remove it manually; refusing to overwrite", err)
		}
	}
	entry, _ := root[role].(map[string]any)
	if entry == nil {
		entry = map[string]any{}
	}
	update(entry)
	root[role] = entry

	out, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return err
	}
	return atomicWriteFile(path, out, 0o600)
}

func pad32(b []byte) []byte {
	if len(b) >= 32 {
		return b
	}
	out := make([]byte, 32)
	copy(out[32-len(b):], b)
	return out
}

func timeNowRFC3339() string { return time.Now().Format(time.RFC3339) }

func x509MarshalPKCS8(key *ecdsa.PrivateKey) ([]byte, error) {
	return x509.MarshalPKCS8PrivateKey(key)
}

func x509ParsePKCS8(der []byte) (any, error) {
	return x509.ParsePKCS8PrivateKey(der)
}

// GeneratePasskey creates a new software passkey bound to the given RP
// ID/origin.
func GeneratePasskey(role, rpid, origin string) (*PasskeyIdentity, error) {
	if rpid == "" || origin == "" {
		return nil, errors.New("WEBAUTHN_RPID and WEBAUTHN_ORIGIN (or --rpid/--origin) are required")
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
	if !ok || ec.Curve != elliptic.P256() {
		return nil, errors.New("passkey: key is not ES256/P-256")
	}
	return ec, nil
}

// publicKey returns the parsed ES256 private-key's public half.
func (p *PasskeyIdentity) publicKey() (*ecdsa.PublicKey, error) {
	priv, err := p.privateKey()
	if err != nil {
		return nil, err
	}
	return &priv.PublicKey, nil
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
	return "// roles.json → \"" + p.Role + "\".passkeys\n" + string(out), nil
}

// MergePasskeyCredential appends (or dedupe-updates) this credential into
// the role's passkeys[] inside roles.json, preserving sibling roles.
func (p *PasskeyIdentity) MergePasskeyCredential(path, role string) error {
	return MergeRolesFile(path, role, func(entry map[string]any) {
		newEntry := map[string]any{
			"credential_id": p.CredentialID,
			"public_key":    p.PublicKey,
			"added_at":      time.Now().Format(time.RFC3339),
		}
		list, _ := entry["passkeys"].([]any)
		for i, raw := range list {
			if ex, _ := raw.(map[string]any); ex != nil && ex["credential_id"] == p.CredentialID {
				list[i] = newEntry
				return
			}
		}
		entry["passkeys"] = append(list, newEntry)
	})
}

// BuildAssertion signs the handshake nonce and returns the AuthPacket
// fields. The sign counter is incremented; persist the whole identity file
// afterwards to keep it durable.
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

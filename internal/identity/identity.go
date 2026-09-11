// Package identity holds the locally stored login identities shared by the
// chat client and the v2vctl management tool: a classic ed25519 key-file
// slot inside one versioned container.
package identity

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

const Version = 3

type Ed25519Identity struct {
	Role         string `json:"role"`
	PrivateKey   string `json:"private_key"`             // hex-encoded seed
	HmacShield   string `json:"hmac_shield"`             // hex-encoded
	ServerPubKey string `json:"server_pubkey,omitempty"` // hex ed25519 pub of server, anti-phishing pin
}

type IdentityFile struct {
	Version int              `json:"version"`
	Ed25519 *Ed25519Identity `json:"ed25519,omitempty"`
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
	// Reject old Host-based files (no backward compat).
	if ed, ok := probe["ed25519"].(map[string]any); ok {
		if _, hasHost := ed["host"]; hasHost {
			return nil, errors.New("key file uses old Host pinning — run v2vctl migrate to update to server_pubkey")
		}
	}
	if _, isContainer := probe["version"]; isContainer {
		var f IdentityFile
		if err := json.Unmarshal(data, &f); err != nil {
			return nil, err
		}
		if f.Version < Version {
			return nil, errors.New("key file version too old — run v2vctl migrate")
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
func LoadEncrypted(path string, passphrase []byte) (*IdentityFile, error) {
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
func (f *IdentityFile) SaveEncrypted(path string, passphrase []byte, p *Params) error {
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

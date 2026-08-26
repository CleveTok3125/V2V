package main

// IdentityFile is the v2 container for locally stored login identities.
// Per the one-slot-per-type rule it holds at most one ed25519 key-file
// identity and one software passkey; regenerating a flavor replaces its own
// slot and never touches the sibling.

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

const identityFileVersion = 2

type Ed25519Identity struct {
	Role       string `json:"role"`
	PrivateKey string `json:"private_key"` // hex-encoded seed
	HmacShield string `json:"hmac_shield"` // hex-encoded
}

type IdentityFile struct {
	Version int              `json:"version"`
	Ed25519 *Ed25519Identity `json:"ed25519,omitempty"`
	Passkey *PasskeyIdentity `json:"passkey,omitempty"`
}

// LoadIdentityFile reads key.json in either the current v2 container shape or
// the legacy flat ed25519 shape (read-compat for files already in the wild).
func LoadIdentityFile(path string) (*IdentityFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var probe map[string]any
	if json.Unmarshal(data, &probe) != nil {
		return nil, errors.New("key.json không phải JSON hợp lệ")
	}
	if _, isContainer := probe["version"]; isContainer {
		var f IdentityFile
		if err := json.Unmarshal(data, &f); err != nil {
			return nil, err
		}
		f.Version = identityFileVersion
		return &f, nil
	}
	// Legacy flat ed25519 file.
	var legacy Ed25519Identity
	if err := json.Unmarshal(data, &legacy); err != nil {
		return nil, errors.New("key.json sai định dạng")
	}
	if legacy.Role == "" || legacy.PrivateKey == "" || legacy.HmacShield == "" {
		return nil, errors.New("key.json thiếu trường bắt buộc")
	}
	return &IdentityFile{Version: identityFileVersion, Ed25519: &legacy}, nil
}

// Save writes the container with owner-only permissions.
func (f *IdentityFile) Save(path string) error {
	f.Version = identityFileVersion
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

// mergeRolesFile applies update() to a single role entry inside roles.json,
// preserving every other top-level role. A file that exists but cannot be
// parsed aborts the operation instead of being clobbered.
func mergeRolesFile(path, role string, update func(entry map[string]any)) error {
	root := map[string]any{}
	if data, err := os.ReadFile(path); err == nil && len(data) > 0 {
		if err := json.Unmarshal(data, &root); err != nil {
			return fmt.Errorf("roles.json không đọc được (%v) — sửa hoặc xóa tay, không tự ghi đè", err)
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
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, out, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// pickIdentity resolves which slot to use when key.json holds both flavors.
func pickIdentity(f *IdentityFile) (useEd, usePk bool) {
	return pickIdentityFrom(os.Stdin, f)
}

// pickIdentityFrom is the injectable core of pickIdentity. Interactive
// sessions get a numbered menu; a non-interactive reader (piped, closed)
// prefers the passkey and falls back to ed25519.
func pickIdentityFrom(r io.Reader, f *IdentityFile) (useEd, usePk bool) {
	hasEd, hasPk := f.Ed25519 != nil, f.Passkey != nil
	switch {
	case hasEd && hasPk:
	default:
		return hasEd, hasPk // only one slot filled
	}

	fmt.Println("key.json chứa 2 danh tính — chọn loại đăng nhập:")
	fmt.Println("  [1] ed25519 key-file  role: " + f.Ed25519.Role)
	fmt.Println("  [2] passkey           role: " + f.Passkey.Role)
	fmt.Print("Chọn (1/2, Enter = passkey): ")

	line, err := bufio.NewReader(r).ReadString('\n')
	if err != nil && line == "" {
		return false, true // stdin closed: deterministic default
	}
	switch strings.TrimSpace(line) {
	case "1":
		return true, false
	case "2":
		return false, true
	default:
		return false, true
	}
}

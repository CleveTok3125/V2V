package main

// Login-side glue over the shared identity package: the interactive picker
// used when key.json holds both flavors.

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"

	"localchat/identity"
)

type (
	Ed25519Identity = identity.Ed25519Identity
	PasskeyIdentity = identity.PasskeyIdentity
	IdentityFile    = identity.IdentityFile
)

// LoadIdentityFile reads key.json (v2 container or legacy flat ed25519).
func LoadIdentityFile(path string) (*IdentityFile, error) {
	return identity.Load(path)
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

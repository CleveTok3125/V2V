package main

// Login-side glue over the shared identity package: the interactive picker
// used when key.json holds both flavors.

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/CleveTok3125/V2V/identity"

	xterm "github.com/charmbracelet/x/term"
)

type (
	Ed25519Identity = identity.Ed25519Identity
	PasskeyIdentity = identity.PasskeyIdentity
	IdentityFile    = identity.IdentityFile
)

// LoadIdentityFile reads key.json, handling encrypted files (version 3).
func LoadIdentityFile(path string) (*IdentityFile, error) {
	if enc, _ := identity.IsEncrypted(path); enc {
		if pass := os.Getenv("V2V_PASSPHRASE"); pass != "" {
			return identity.LoadEncrypted(path, pass)
		}
		// Prompt for passphrase (hidden input)
		fmt.Print("🔒 Nhập passphrase cho key file: ")
		// Try to use term.ReadPassword if available, fallback to plain
		pass, err := readPassphrase()
		fmt.Println()
		if err != nil {
			return nil, err
		}
		return identity.LoadEncrypted(path, pass)
	}
	return identity.Load(path)
}

var loadedPassphrase string
var loadedWasEncrypted bool

func readPassphrase() (string, error) {
	// Use charmbracelet/x/term to hide input (same stack as v2vctl's huh)
	if xterm.IsTerminal(os.Stdin.Fd()) {
		b, err := xterm.ReadPassword(os.Stdin.Fd())
		if err != nil {
			return "", err
		}
		return string(b), nil
	}
	// Fallback for piped/non-TTY (CI): read full line including spaces
	reader := bufio.NewReader(os.Stdin)
	line, err := reader.ReadString('\n')
	if err != nil && len(line) == 0 {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}

func SaveIdentityFileEncrypted(path string, idf *IdentityFile) error {
	if loadedWasEncrypted && loadedPassphrase != "" {
		return idf.SaveEncrypted(path, loadedPassphrase, nil)
	}
	return idf.Save(path)
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

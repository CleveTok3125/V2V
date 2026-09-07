package main

// Login-side glue over the shared identity package: the interactive picker
// used when key.json holds both flavors.

import (
	"fmt"
	"io"
	"os"

	"github.com/CleveTok3125/V2V/identity"

	"github.com/CleveTok3125/V2V/internal/passprompt"
	"github.com/CleveTok3125/V2V/internal/tui"
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
			// Env unlock only warns when weak: refusing here would
			// lock out legitimate files. No personal context feeds
			// this check; it still catches trivial secrets.
			if AssessPassphrase(pass, nil).Weak {
				fmt.Println("⚠️ V2V_PASSPHRASE yếu, cân nhắc đổi.")
			}
			pw := []byte(pass)
			pass = ""
			defer identity.ZeroBytes(pw)
			return identity.LoadEncrypted(path, pw)
		}
		// Prompt for passphrase (hidden input). TTY sessions use the
		// shared program; piped input keeps the legacy hidden reader.
		var pass string
		var err error
		if tui.Interactive() {
			pass, err = passprompt.Password(passprompt.PasswordOpts{
				Title: "🔒 Nhập passphrase cho key file",
			})
		} else {
			fmt.Print("🔒 Nhập passphrase cho key file: ")
			// Try to use term.ReadPassword if available, fallback to plain
			pass, err = readPassphrase()
			fmt.Println()
		}
		if err != nil {
			return nil, err
		}
		pw := []byte(pass)
		pass = ""
		defer identity.ZeroBytes(pw)
		return identity.LoadEncrypted(path, pw)
	}
	return identity.Load(path)
}

var loadedPassphrase []byte
var loadedWasEncrypted bool

func readPassphrase() (string, error) {
	// Use charmbracelet/x/term to hide input (same stack as v2vctl's huh)
	if tui.Interactive() {
		b, err := xterm.ReadPassword(os.Stdin.Fd())
		if err != nil {
			return "", err
		}
		defer identity.ZeroBytes(b)
		return string(b), nil
	}
	// Fallback for piped/non-TTY (CI): read full line including spaces
	return tui.ReadLine(os.Stdin)
}

func SaveIdentityFileEncrypted(path string, idf *IdentityFile) error {
	if loadedWasEncrypted && len(loadedPassphrase) != 0 {
		return idf.SaveEncrypted(path, loadedPassphrase, nil)
	}
	return idf.Save(path)
}

// pickIdentity resolves which slot to use when key.json holds both flavors.
func pickIdentity(f *IdentityFile) (useEd, usePk bool) {
	return pickIdentityFrom(os.Stdin, f)
}

// pickIdentityFrom is the injectable core of pickIdentity. Both slots
// filled asks via the shared tui select: huh form defaulting to
// passkey on TTY, numbered menu with the same default on pipes. r
// feeds the piped path so tests stay headless; a single filled slot
// returns without prompting.
func pickIdentityFrom(r io.Reader, f *IdentityFile) (useEd, usePk bool) {
	hasEd, hasPk := f.Ed25519 != nil, f.Passkey != nil
	switch {
	case hasEd && hasPk:
	default:
		return hasEd, hasPk // only one slot filled
	}
	title := "key.json chứa 2 danh tính — chọn loại đăng nhập:"
	options := []string{
		"ed25519 key-file  role: " + f.Ed25519.Role,
		"passkey           role: " + f.Passkey.Role,
	}
	var idx int
	if tui.Interactive() {
		var err error
		idx, err = tui.Select(title, options, 1)
		if err != nil {
			return false, true // aborted: deterministic default
		}
	} else {
		idx, _ = tui.SelectPiped(r, title, options, 1)
	}
	if idx == 0 {
		return true, false
	}
	return false, true
}

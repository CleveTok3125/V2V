package main

// Login-side glue over the shared identity package: encrypted key.json
// unlock with hidden prompt and deep-link TTY fallback.

import (
	"fmt"
	"os"

	"github.com/CleveTok3125/V2V/internal/env"
	"github.com/CleveTok3125/V2V/internal/identity"

	"github.com/CleveTok3125/V2V/internal/passprompt"
	"github.com/CleveTok3125/V2V/internal/tui"
	xterm "github.com/charmbracelet/x/term"
)

type (
	Ed25519Identity = identity.Ed25519Identity
	IdentityFile    = identity.IdentityFile
)

// LoadIdentityFile reads key.json, handling encrypted files (version 3).
func LoadIdentityFile(path string) (*IdentityFile, error) {
	if enc, _ := identity.IsEncrypted(path); enc {
		if pass := env.Passphrase(); pass != "" {
			// Env unlock only warns when weak: refusing here would
			// lock out legitimate files. No personal context feeds
			// this check; it still catches trivial secrets.
			if AssessPassphrase(pass, nil).Weak {
				fmt.Println("⚠️ V2V_PASSPHRASE yếu, cân nhắc đổi.")
			}
			pw := []byte(pass)
			pass = ""
			defer identity.ZeroBytes(pw)
			idf, err := identity.LoadEncrypted(path, pw)
			if err != nil {
				return nil, err
			}
			rememberLoadedPassphrase(pw)
			return idf, nil
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
		idf, err := identity.LoadEncrypted(path, pw)
		if err != nil {
			return nil, err
		}
		rememberLoadedPassphrase(pw)
		return idf, nil
	}
	return identity.Load(path)
}

var loadedPassphrase []byte
var loadedWasEncrypted bool

// rememberLoadedPassphrase keeps a copy of the successful unlock secret
// so session saves (passkey counters) re-encrypt instead of silently
// dropping to plaintext. Wiped by ClearLoadedPassphrase at session end.
func rememberLoadedPassphrase(pw []byte) {
	ClearLoadedPassphrase()
	loadedPassphrase = append([]byte(nil), pw...)
	loadedWasEncrypted = true
}

// ClearLoadedPassphrase wipes the remembered unlock secret. Call at
// session end (deferred next to term.Close, and on the conn-drop exit
// path which skips defers).
func ClearLoadedPassphrase() {
	identity.ZeroBytes(loadedPassphrase)
	loadedPassphrase = nil
	loadedWasEncrypted = false
}

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

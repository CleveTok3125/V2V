package main

// Login-side glue over the shared identity package: the client's Prompter
// implementation, the remembered unlock secret, and the save path that uses
// it. The unlock flow itself lives in lib/clientcore.

import (
	"fmt"
	"os"

	"github.com/CleveTok3125/V2V/lib/clientcore"
	"github.com/CleveTok3125/V2V/lib/identity"

	"github.com/CleveTok3125/V2V/internal/passprompt"
	"github.com/CleveTok3125/V2V/internal/tui"
	xterm "github.com/charmbracelet/x/term"
)

type (
	Ed25519Identity = identity.Ed25519Identity
	IdentityFile    = identity.IdentityFile
)

// cliPrompter is the terminal Prompter: a hidden prompt on a TTY, and the
// legacy plain reader when input is piped (CI).
type cliPrompter struct{}

func (cliPrompter) Secret(reason clientcore.SecretReason) (string, error) {
	switch reason {
	case clientcore.ReasonKeyFilePassphrase:
		const title = "🔒 Nhập passphrase cho key file"
		if tui.Interactive() {
			return passprompt.Password(passprompt.PasswordOpts{Title: title})
		}
		fmt.Print(title + ": ")
		pass, err := readPassphrase()
		fmt.Println()
		return pass, err
	default:
		return "", fmt.Errorf("clientcore: unknown secret reason %d", reason)
	}
}

// warnWeakPassphrase is the note the core cannot make: the strength policy
// lives here.
func warnWeakPassphrase(pass string) {
	if AssessPassphrase(pass, nil).Weak {
		fmt.Println("⚠️ V2V_PASSPHRASE yếu, cân nhắc đổi.")
	}
}

// LoadIdentityFile reads key.json, handling encrypted files (version 3),
// and keeps the unlock secret for later saves.
func LoadIdentityFile(path string) (*IdentityFile, error) {
	idf, pw, err := clientcore.LoadIdentityFile(path, cliPrompter{}, warnWeakPassphrase)
	if err != nil {
		return nil, err
	}
	if len(pw) != 0 {
		rememberLoadedPassphrase(pw)
		identity.ZeroBytes(pw)
	}
	return idf, nil
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

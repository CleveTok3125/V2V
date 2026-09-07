package main

// Login-side glue over the shared identity package: the interactive picker
// used when key.json holds both flavors.

import (
	"fmt"
	"io"
	"os"
	"strings"

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
			return identity.LoadEncrypted(path, pass)
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
		return identity.LoadEncrypted(path, pass)
	}
	return identity.Load(path)
}

var loadedPassphrase string
var loadedWasEncrypted bool

// readLineRaw reads one line without read-ahead: byte-by-byte, so bytes
// meant for later readers (prompts, then readline's chat loop) stay on
// the fd. Buffered readers would swallow piped input past the newline.
func readLineRaw(r io.Reader) (string, error) {
	var buf []byte
	one := make([]byte, 1)
	for {
		n, err := r.Read(one)
		if n > 0 {
			if one[0] == '\n' {
				break
			}
			buf = append(buf, one[0])
		}
		if err != nil {
			if len(buf) == 0 {
				return "", err
			}
			break
		}
	}
	return strings.TrimRight(string(buf), "\r"), nil
}

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
	return readLineRaw(os.Stdin)
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

package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/CleveTok3125/V2V/internal/env"
	"github.com/CleveTok3125/V2V/internal/identity"
	"github.com/CleveTok3125/V2V/internal/passprompt"
	"github.com/CleveTok3125/V2V/internal/strength"
	"github.com/CleveTok3125/V2V/internal/tui"
)

// toPromptAssessment adapts the policy Report to the prompt meter.
// The adapter lives caller-side so strength never imports UI packages.
func toPromptAssessment(r strength.Report) passprompt.Assessment {
	return passprompt.Assessment{Bits: r.Bits, Score: r.Score, Capped: r.Capped, Label: r.Label, Weak: r.Weak}
}

func nonEmpty(s string) error {
	if strings.TrimSpace(s) == "" {
		return errors.New("bắt buộc")
	}
	return nil
}

func promptPassphrase() (string, error) {
	// Single entry first so a weak passphrase warns and confirms
	// before the re-entry round, not after it.
	pass, err := passprompt.Password(passprompt.PasswordOpts{
		Title:      "Passphrase (Enter = không mã hóa)",
		AllowEmpty: true,
		MaxRounds:  passprompt.DefaultMaxRounds,
		Assess:     func(s string) passprompt.Assessment { return toPromptAssessment(strength.Assess(s, nil)) },
	})
	if err != nil || strings.TrimSpace(pass) == "" {
		return pass, err
	}
	if strength.Assess(pass, nil).Weak {
		fmt.Println("⚠️ Passphrase yếu — file mã hóa dễ bị bẻ nếu lọt ra ngoài.")
		if tui.HasControllingTTY() {
			ok, err := tui.Confirm("Vẫn dùng passphrase này?")
			if err != nil || !ok {
				return "", errors.New("đã hủy passphrase yếu")
			}
		}
	}
	if _, err := passprompt.Password(passprompt.PasswordOpts{
		ConfirmTitle: "Nhập lại passphrase",
		MaxRounds:    passprompt.DefaultMaxRounds,
		Expect:       pass,
	}); err != nil {
		return "", err
	}
	return pass, nil
}


func loadContainer(path string) (*identity.IdentityFile, error) {
	// Check if file is encrypted and need passphrase
	if enc, _ := identity.IsEncrypted(path); enc {
		// Try env first
		if pass := env.Passphrase(); pass != "" {
			pw := []byte(pass)
			pass = ""
			idf, err := identity.LoadEncrypted(path, pw)
			identity.ZeroBytes(pw)
			return idf, err
		}
		if tui.HasControllingTTY() {
			fmt.Println("🔒 File đã mã hóa, nhập passphrase để mở...")
			pass, err := promptPassphraseForLoad()
			if err != nil {
				return nil, err
			}
			pw := []byte(pass)
			pass = ""
			idf, err := identity.LoadEncrypted(path, pw)
			identity.ZeroBytes(pw)
			return idf, err
		}
		return nil, errors.New("key file is encrypted — set V2V_PASSPHRASE or run in TTY to unlock")
	}
	idf, err := identity.Load(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &identity.IdentityFile{}, nil
		}
		return nil, err
	}
	return idf, nil
}

func promptPassphraseForLoad() (string, error) {
	// Empty can never unlock: retry until non-empty, like the old
	// huh Validate(nonEmpty) loop. Esc aborts via passprompt.
	for {
		pass, err := passprompt.Password(passprompt.PasswordOpts{
			Title:      "Nhập passphrase",
			AllowEmpty: true,
		})
		if err != nil {
			return "", err
		}
		if strings.TrimSpace(pass) != "" {
			return pass, nil
		}
		fmt.Println("❌ bắt buộc, nhập lại.")
	}
}

func saveContainer(idf *identity.IdentityFile, path string) error {
	if tui.HasControllingTTY() {
		pass, err := promptPassphrase()
		if err != nil {
			return err
		}
		if pass != "" {
			pw := []byte(pass)
			pass = ""
			err := idf.SaveEncrypted(path, pw, nil)
			identity.ZeroBytes(pw)
			return err
		}
	}
	// Check env for non-interactive
	if pass := env.Passphrase(); pass != "" {
		if strength.Assess(pass, nil).Weak {
			fmt.Println("⚠️ V2V_PASSPHRASE yếu, cân nhắc đổi.")
		}
		pw := []byte(pass)
		pass = ""
		err := idf.SaveEncrypted(path, pw, nil)
		identity.ZeroBytes(pw)
		return err
	}
	return idf.Save(path)
}

// rolesPath is the single canonical location. No fallback: unmigrated
// deploys fail closed instead of silently using defaults.
func rolesPath() string { return filepath.Join("config", "roles.json") }

// --- keygen ed25519 -----------------------------------------------------


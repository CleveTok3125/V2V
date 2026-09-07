package main

import (
	"errors"
	"fmt"
	"os"
	"encoding/json"
	"path/filepath"

	"github.com/CleveTok3125/V2V/identity"
	"github.com/CleveTok3125/V2V/internal/strength"
	"github.com/CleveTok3125/V2V/internal/tui"
	"github.com/charmbracelet/huh"
)

type MigrateCmd struct {
	In     string `help:"File nguồn" default:"key.json"`
	Out    string `help:"File đích (mặc định ghi đè In, backup .old)"`
	Preset string `help:"Preset đích: native, wasm, custom" default:"native"`
	Force  bool   `help:"Ghi đè không hỏi khi .old đã tồn tại"`
}

func (m *MigrateCmd) Run() error {
	inPath := m.In
	outPath := m.Out
	if outPath == "" {
		outPath = inPath
	}
	// Detect current file status
	enc, _ := identity.IsEncrypted(inPath)
	var curPreset string
	if enc {
		data, _ := os.ReadFile(inPath)
		var env struct {
			Encrypted struct {
				Memory uint32 `json:"m"`
				Time   uint32 `json:"t"`
			} `json:"encrypted"`
		}
		_ = json.Unmarshal(data, &env)
		if env.Encrypted.Memory == 32768 {
			curPreset = "wasm (t=1,m=32MB)"
		} else {
			curPreset = fmt.Sprintf("native (t=%d,m=%d)", env.Encrypted.Time, env.Encrypted.Memory)
		}
	} else {
		curPreset = "plaintext (không mã hóa)"
	}

	if tui.HasControllingTTY() {
		// Distinct TUI: show current status, then choose preset, then passphrases
		var presetChoice string
		form := huh.NewForm(huh.NewGroup(
			huh.NewSelect[string]().Title("Preset hiện tại: "+curPreset+" → Chọn preset đích").Options(
				huh.NewOption("Native — t=3,m=64MB, XChaCha (mạnh)", "native"),
				huh.NewOption("WASM — t=1,m=32MB, XChaCha (nhanh, cho browser)", "wasm"),
				huh.NewOption("Custom", "custom"),
			).Value(&presetChoice),
		))
		if err := form.Run(); err != nil {
			return err
		}
		if presetChoice != "" {
			m.Preset = presetChoice
		}
	}

	// Handle In -> .old backup
	oldPath := inPath + ".old"
	if _, err := os.Stat(oldPath); err == nil {
		if !m.Force {
			if tui.HasControllingTTY() {
				var overwrite bool
				confirm := huh.NewForm(huh.NewGroup(
					huh.NewConfirm().Title(fmt.Sprintf("%s đã tồn tại — ghi đè?", oldPath)).Affirmative("Ghi đè").Negative("Hủy").Value(&overwrite),
				))
				if err := confirm.Run(); err != nil {
					return err
				}
				if !overwrite {
					return errors.New("hủy migrate")
				}
			} else {
				return fmt.Errorf("%s đã tồn tại, dùng --force để ghi đè", oldPath)
			}
		}
		fmt.Printf("⚠️  Ghi đè %s\n", oldPath)
	}

	// Load with old passphrase if needed
	var idf *identity.IdentityFile
	var oldPass string
	if enc {
		if p := os.Getenv("V2V_PASSPHRASE"); p != "" {
			oldPass = p
		} else if tui.HasControllingTTY() {
			fmt.Println("🔒 File đã mã hóa, nhập passphrase hiện tại để mở...")
			var err error
			oldPass, err = promptPassphraseForLoad()
			if err != nil {
				return err
			}
		} else {
			return errors.New("key file is encrypted — set V2V_PASSPHRASE")
		}
		var err error
		oldPw := []byte(oldPass)
		oldPass = ""
		idf, err = identity.LoadEncrypted(inPath, oldPw)
		identity.ZeroBytes(oldPw)
		if err != nil {
			return fmt.Errorf("sai passphrase hoặc file hỏng: %w", err)
		}
	} else {
		var err error
		idf, err = identity.Load(inPath)
		if err != nil {
			return err
		}
	}

	// Determine new preset
	var p *identity.Params
	switch m.Preset {
	case "wasm":
		pp := identity.PresetWASM
		p = &pp
	case "native":
		pp := identity.PresetNative
		p = &pp
	case "custom":
		// For custom, prompt for t/m/p if interactive
		if tui.HasControllingTTY() {
			var tStr, mStr string
			form := huh.NewForm(huh.NewGroup(
				huh.NewInput().Title("Time (t)").Value(&tStr),
				huh.NewInput().Title("Memory KiB (m)").Value(&mStr),
			))
			if err := form.Run(); err != nil {
				return err
			}
			// Parse and set custom, fallback to native if invalid
			pp := identity.PresetNative
			if tStr != "" {
				if _, err := fmt.Sscanf(tStr, "%d", &pp.Time); err != nil {
					return fmt.Errorf("Time (t) không hợp lệ %q: %w", tStr, err)
				}
			}
			if mStr != "" {
				if _, err := fmt.Sscanf(mStr, "%d", &pp.Memory); err != nil {
					return fmt.Errorf("Memory KiB (m) không hợp lệ %q: %w", mStr, err)
				}
			}
			p = &pp
		}
	default:
		pp := identity.PresetNative
		p = &pp
	}

	// Prompt for new passphrase if target is encrypted (preset != plaintext)
	// For migrate, we always re-encrypt (unless user wants plaintext)
	var newPass string
	if tui.HasControllingTTY() {
		fmt.Println("🔒 Nhập passphrase mới cho file đích (Enter = không mã hóa):")
		var err error
		newPass, err = promptPassphrase()
		if err != nil {
			return err
		}
	} else if pass := os.Getenv("V2V_PASSPHRASE"); pass != "" {
		if strength.Assess(pass, nil).Weak {
			fmt.Println("⚠️ V2V_PASSPHRASE yếu, cân nhắc đổi.")
		}
		newPass = pass
	}

	// Write to temp out path first
	tmpOut := outPath
	if outPath == inPath {
		tmpOut = inPath + ".tmp-migrate"
	}
	var err error
	if newPass != "" {
		newPw := []byte(newPass)
		newPass = ""
		err = idf.SaveEncrypted(tmpOut, newPw, p)
		identity.ZeroBytes(newPw)
	} else {
		err = idf.Save(tmpOut)
	}
	if err != nil {
		return err
	}

	// Atomic In -> .old and Out -> In
	if err := os.Rename(inPath, oldPath); err != nil {
		os.Remove(tmpOut)
		return fmt.Errorf("không thể backup %s -> %s: %w", inPath, oldPath, err)
	}
	if err := os.Rename(tmpOut, inPath); err != nil {
		// Try to restore backup on failure
		_ = os.Rename(oldPath, inPath)
		os.Remove(tmpOut)
		return err
	}
	// Fsync dir
	if dirFile, err := os.Open(filepath.Dir(inPath)); err == nil {
		_ = dirFile.Sync()
		dirFile.Close()
	}
	fmt.Printf("✅ Đã migrate %s -> %s (backup %s), preset %s\n", inPath, inPath, oldPath, m.Preset)
	return nil
}

// --- list -----------------------------------------------------------------

 // waPending mirrors the server's pending-ticket schema.

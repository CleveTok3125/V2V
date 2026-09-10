//go:build !js

package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/alecthomas/kong"

	"github.com/CleveTok3125/V2V/internal/identity"
	"github.com/CleveTok3125/V2V/internal/config"
	"github.com/CleveTok3125/V2V/internal/configdir"
	"github.com/CleveTok3125/V2V/internal/env"
	"github.com/CleveTok3125/V2V/internal/passprompt"
	"github.com/CleveTok3125/V2V/internal/tui"
)

var ClientCfg *config.ClientConfig

func parseFlags() {
	kong.Parse(&CLI, kong.Vars{
		"version": Version,
	})
	if CLI.ConfigDir == "" {
		CLI.ConfigDir = configdir.DefaultConfigDir()
	}
	if CLI.CacheDir == "" {
		CLI.CacheDir = configdir.DefaultCacheDir()
	}
	if CLI.KeyFile != "" {
		// Explicit path wins (breaks old -k <path>, now -K/--key-file)
	} else if CLI.UseKey {
		CLI.KeyFile = filepath.Join(CLI.ConfigDir, "key.json")
	} else {
		CLI.KeyFile = ""
	}
	historyFile = filepath.Join(CLI.CacheDir, "history.tmp")
	// Client config is immutable state: read freely, replaced only by
	// explicit actions. A missing file means in-memory defaults; copy
	// template/config.jsonc to the config dir to customize.
	cfgPath := resolveCfgPath()
	if cfg, err := config.Load(cfgPath); err == nil {
		ClientCfg = cfg
	} else if errors.Is(err, config.ErrEncrypted) {
		if cfg, err := loadEncryptedConfig(cfgPath); err == nil {
			ClientCfg = cfg
		} else {
			fmt.Printf("❌ Không mở được config mã hóa: %v\n", err)
			os.Exit(1)
		}
	} else {
		ClientCfg = config.DefaultClientConfig()
	}
}

// resolveCfgPath returns the single canonical config path: config.jsonc.
// Plain .json is not read; JSONC (comments allowed, no trailing commas)
// is the only config format.
func resolveCfgPath() string {
	cfgPath := configdir.DefaultConfigFile(CLI.ConfigDir)
	if _, err := os.Stat(cfgPath); err != nil {
		fmt.Printf("config %s not found, using defaults (see template/config.jsonc)\n", cfgPath)
	}
	return cfgPath
}

// encryptConfigFile seals the resolved config with a passphrase and
// atomically replaces it. One-shot for --encrypt-config; the chat path
// never writes config on its own.
func encryptConfigFile() {
	path := resolveCfgPath()
	data, err := os.ReadFile(path)
	if err != nil {
		fmt.Printf("❌ Không đọc được config: %v\n", err)
		os.Exit(1)
	}
	if identity.IsEncryptedData(data) {
		fmt.Println("❌ File đã mã hóa rồi")
		os.Exit(1)
	}
	pass := env.Passphrase()
	if pass == "" {
		if !tui.Interactive() {
			fmt.Println("❌ set V2V_PASSPHRASE or run in TTY to encrypt")
			os.Exit(1)
		}
		assess := func(s string) passprompt.Assessment {
			return toAssessment(AssessPassphrase(s, nil))
		}
		if pass, err = passprompt.Password(passprompt.PasswordOpts{
			Title:  "🔒 Passphrase mã hóa config",
			Assess: assess,
		}); err != nil {
			fmt.Printf("❌ %v\n", err)
			os.Exit(1)
		}
		if _, err := passprompt.Password(passprompt.PasswordOpts{
			ConfirmTitle: "🔒 Nhập lại để xác nhận",
			Expect:       pass,
		}); err != nil {
			fmt.Printf("❌ %v\n", err)
			os.Exit(1)
		}
	}
	pw := []byte(pass)
	pass = ""
	sealed, err := identity.EncryptData(data, pw)
	identity.ZeroBytes(pw)
	if err != nil {
		fmt.Printf("❌ Không mã hóa được: %v\n", err)
		os.Exit(1)
	}
	if err := config.ReplaceFile(path, sealed); err != nil {
		fmt.Printf("❌ Không ghi được config: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Đã mã hóa %s (giữ passphrase an toàn)\n", path)
}

// loadEncryptedConfig mirrors the tripcode unlock: V2V_PASSPHRASE first,
// interactive prompt second, hard error without a TTY.
func loadEncryptedConfig(path string) (*config.ClientConfig, error) {
	unlock := env.Passphrase()
	if unlock == "" {
		if !tui.Interactive() {
			return nil, errors.New("config is encrypted — set V2V_PASSPHRASE or run in TTY to unlock")
		}
		var err error
		unlock, err = passprompt.Password(passprompt.PasswordOpts{
			Title: "🔒 Nhập passphrase mở config",
		})
		if err != nil {
			return nil, err
		}
	}
	pw := []byte(unlock)
	unlock = ""
	cfg, err := config.LoadEncrypted(path, pw)
	identity.ZeroBytes(pw)
	return cfg, err
}

// applyWebPasskey is web-only: the desktop signs assertions natively from
// its -k identity file, so this path never engages here. Returning true
// simply means "nothing failed; continue as configured".
func applyWebPasskey(*AuthPacket, string) bool {
	return true
}

func setWasmStatus(string, bool) bool { return false }

func showWasmStatus(string, bool) bool { return false }

func parkForever() {}

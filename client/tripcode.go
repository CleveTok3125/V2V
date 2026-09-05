package main

// Tripcode secret handling (desktop): the -t flag takes no value. The
// secret resolves as V2V_TRIPCODE env > tripcode.json in the config dir
// (encrypted v3 envelope only, never plaintext) > hidden interactive
// prompt. First manual entry offers an encrypted save. WASM keeps its
// JS-provided string and never touches this flow (see resolveTripcode).

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/CleveTok3125/V2V/identity"
)

// TripcodeFileName is the secret file inside the config dir.
const TripcodeFileName = "tripcode.json"

// tripcodeFile is the on-disk shape. Only the encrypted envelope is
// accepted; a plaintext file is refused fail-closed.
type tripcodeFile struct {
	Version  int    `json:"version"`
	Tripcode string `json:"tripcode"`
}

// resolveTripcode returns the tripcode string for this session.
// Empty + nil error means "no tripcode requested" only when useFlag is
// false; callers pass the -t state. WASM returns the JS-provided value.
func resolveTripcode(useFlag bool, configDir string) (string, error) {
	if isWASMRuntime() {
		return CLI.Tripcode, nil
	}
	if !useFlag {
		return "", nil
	}
	if v := os.Getenv("V2V_TRIPCODE"); v != "" {
		return v, nil
	}
	path := filepath.Join(configDir, TripcodeFileName)
	if tc, found, err := loadTripcodeFile(path); err != nil {
		return "", err
	} else if found {
		return tc, nil
	}
	fmt.Print("🔑 Nhập tripcode: ")
	tc, err := readPassphrase()
	fmt.Println()
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(tc) == "" {
		return "", errors.New("tripcode trống")
	}
	if offerTripcodeSave(os.Stdin) {
		if err := saveTripcodePrompt(path, tc); err != nil {
			fmt.Printf("⚠️ Không lưu được tripcode: %v\n", err)
		} else {
			fmt.Println("💾 Đã lưu tripcode mã hóa.")
		}
	}
	return tc, nil
}

// loadTripcodeFile reads the secret file. found=false when absent.
// Plaintext files are refused: tripcode at rest is always encrypted.
func loadTripcodeFile(path string) (tc string, found bool, err error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", false, nil
		}
		return "", false, err
	}
	if !identity.IsEncryptedData(data) {
		return "", false, errors.New("tripcode.json không được mã hóa — xóa file hoặc nhập tay để lưu lại bản mã hóa")
	}
	unlock := os.Getenv("V2V_PASSPHRASE")
	if unlock == "" {
		fmt.Print("🔒 Nhập passphrase mở tripcode: ")
		unlock, err = readPassphrase()
		fmt.Println()
		if err != nil {
			return "", false, err
		}
	}
	plain, err := identity.DecryptData(data, unlock)
	if err != nil {
		return "", false, fmt.Errorf("không mở được tripcode.json: %w", err)
	}
	var f tripcodeFile
	if err := json.Unmarshal(plain, &f); err != nil {
		return "", false, errors.New("tripcode.json hỏng")
	}
	if f.Tripcode == "" {
		return "", false, errors.New("tripcode.json trống")
	}
	return f.Tripcode, true, nil
}

// saveTripcodeFile writes the secret encrypted. Empty unlock is refused:
// tripcode at rest is never plaintext.
func saveTripcodeFile(path, tripcode, unlock string) error {
	if unlock == "" {
		return errors.New("cần unlock passphrase để mã hóa")
	}
	plain, err := json.Marshal(tripcodeFile{Version: 3, Tripcode: tripcode})
	if err != nil {
		return err
	}
	enc, err := identity.EncryptData(plain, unlock)
	if err != nil {
		return err
	}
	return identity.AtomicWriteFile(path, enc, 0o600)
}

// saveTripcodePrompt asks for an unlock passphrase (hidden) and saves.
// Empty unlock skips saving without error.
func saveTripcodePrompt(path, tripcode string) error {
	fmt.Print("🔒 Đặt unlock passphrase cho file tripcode (trống = không lưu): ")
	unlock, err := readPassphrase()
	fmt.Println()
	if err != nil {
		return err
	}
	if unlock == "" {
		return errors.New("bỏ qua lưu file")
	}
	return saveTripcodeFile(path, tripcode, unlock)
}

// offerTripcodeSave asks whether to persist a hand-entered tripcode.
// Default is No; unreadable input also means No. Reads are unbuffered
// so piped answers never starve later readers (prompts, chat loop).
func offerTripcodeSave(r io.Reader) bool {
	fmt.Print("Lưu tripcode mã hóa vào file? (y/N): ")
	line, err := readLineRaw(r)
	if err != nil && len(line) == 0 {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes", "có", "co":
		return true
	default:
		return false
	}
}

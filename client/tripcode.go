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
	"github.com/CleveTok3125/V2V/internal/passprompt"
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
// username and serverHost feed the strength meter's personal-info
// context; strength is only ever displayed on the interactive path.
func resolveTripcode(useFlag bool, configDir, username, serverHost string) (string, error) {
	if isWASMRuntime() {
		return CLI.Tripcode, nil
	}
	if !useFlag {
		return "", nil
	}
	ctx := userInputs(username, serverHost)
	if v := os.Getenv("V2V_TRIPCODE"); v != "" {
		if rep := AssessPassphrase(v, ctx); rep.Weak {
			fmt.Printf("⚠️ Tripcode trong env yếu, cân nhắc đổi.\n")
		}
		return v, nil
	}
	path := filepath.Join(configDir, TripcodeFileName)
	if tc, found, err := loadTripcodeFile(path); err != nil {
		return "", err
	} else if found {
		if rep := AssessPassphrase(tc, ctx); rep.Weak {
			fmt.Println(rep.WeakWarning())
		}
		return tc, nil
	}
	fmt.Println(ReminderLine())
	var tc string
	if passprompt.Interactive() {
		var err error
		tc, err = meteredTripcodeEntry(path, ctx)
		if err != nil {
			return "", err
		}
		return tc, nil
	}
	tc, err := readDoubleEntry(readPassphrase)
	if err != nil {
		return "", err
	}
	if len(tc) > 64 {
		return "", errors.New("tripcode quá dài (tối đa 64 byte, server sẽ từ chối)")
	}
	rep := AssessPassphrase(tc, ctx)
	if rep.Weak {
		fmt.Println(rep.WeakWarning())
		if !confirmUseWeak(os.Stdin) {
			return "", errors.New("đã hủy tripcode yếu")
		}
	}
	if offerTripcodeSave(os.Stdin) {
		assessUnlock := func(s string) passprompt.Assessment {
			return toAssessment(AssessPassphrase(s, ctx))
		}
		if err := saveTripcodePrompt(path, tc, assessUnlock); err != nil {
			fmt.Printf("⚠️ Không lưu được tripcode: %v\n", err)
		} else {
			fmt.Println("💾 Đã lưu tripcode mã hóa.")
		}
	}
	return tc, nil
}

// toAssessment maps a strength report to the shared prompt meter. Pure
// so tests pin the mapping without a TTY.
func toAssessment(rep StrengthReport) passprompt.Assessment {
	return passprompt.Assessment{Bits: rep.Entropy, Capped: rep.Capped, Label: rep.Label, Weak: rep.Weak}
}

// meteredTripcodeEntry is the unified TTY flow: single live-meter
// entry, the 64-byte cap, weak warn+confirm, then the encrypted-save
// offer. Re-entry is asked only when the user chooses to save: a
// session-only typo shows up on the badge immediately, while a saved
// typo (or a wrong unlock passphrase) is permanent. ctx feeds the
// meter's personal-info context. Assessment stays a client-side
// callback so passprompt never imports zxcvbn. The piped path keeps
// the upfront double-entry order for script stability.
func meteredTripcodeEntry(path string, ctx []string) (string, error) {
	assess := func(s string) passprompt.Assessment {
		return toAssessment(AssessPassphrase(s, ctx))
	}
	tc, err := passprompt.Password(passprompt.PasswordOpts{
		Title:     "🔑 Nhập tripcode mới",
		MaxRounds: maxEntryAttempts,
		Assess:    assess,
	})
	if err != nil {
		return "", err
	}
	if len(tc) > 64 {
		return "", errors.New("tripcode quá dài (tối đa 64 byte, server sẽ từ chối)")
	}
	rep := AssessPassphrase(tc, ctx)
	if rep.Weak {
		fmt.Println(rep.WeakWarning())
		ok, err := passprompt.Confirm("Vẫn dùng tripcode này?")
		if err != nil || !ok {
			return "", errors.New("đã hủy tripcode yếu")
		}
	}
	ok, err := passprompt.Confirm("Lưu tripcode mã hóa vào file?")
	if err != nil {
		return "", err
	}
	if !ok {
		return tc, nil
	}
	if _, err := passprompt.Password(passprompt.PasswordOpts{
		ConfirmTitle: "🔑 Nhập lại để xác nhận",
		MaxRounds:    maxEntryAttempts,
		Expect:       tc,
	}); err != nil {
		return "", err
	}
	assessUnlock := func(s string) passprompt.Assessment {
		return toAssessment(AssessPassphrase(s, ctx))
	}
	if err := saveTripcodePrompt(path, tc, assessUnlock); err != nil {
		fmt.Printf("⚠️ Không lưu được tripcode: %v\n", err)
	} else {
		fmt.Println("💾 Đã lưu tripcode mã hóa.")
	}
	return tc, nil
}

// userInputs builds zxcvbn personal context from public identity data
// the attacker already knows: display name and server host.
func userInputs(username, serverHost string) []string {
	var out []string
	if u := strings.TrimSpace(username); u != "" {
		out = append(out, u)
	}
	if h := strings.TrimSpace(serverHost); h != "" {
		out = append(out, h)
	}
	return out
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
		if passprompt.Interactive() {
			unlock, err = passprompt.Password(passprompt.PasswordOpts{
				Title: "🔒 Nhập passphrase mở tripcode",
			})
		} else {
			fmt.Print("🔒 Nhập passphrase mở tripcode: ")
			unlock, err = readPassphrase()
			fmt.Println()
		}
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
// Empty unlock skips saving without error. The TTY branch uses
// double-entry with a live meter: a typo here locks the file forever.
// assess maps input to the meter; the piped path reuses the legacy
// single hidden read.
func saveTripcodePrompt(path, tripcode string, assess func(string) passprompt.Assessment) error {
	var unlock string
	var err error
	if passprompt.Interactive() {
		unlock, err = passprompt.Password(passprompt.PasswordOpts{
			Title:        "🔒 Đặt unlock passphrase cho file tripcode (trống = không lưu)",
			ConfirmTitle: "🔒 Nhập lại unlock passphrase",
			Confirm:      true,
			AllowEmpty:   true,
			MaxRounds:    maxEntryAttempts,
			Assess:       assess,
		})
	} else {
		fmt.Print("🔒 Đặt unlock passphrase cho file tripcode (trống = không lưu): ")
		unlock, err = readPassphrase()
		fmt.Println()
	}
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

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CleveTok3125/V2V/lib/config"
)

func writeTripcodeFixture(t *testing.T, dir, tripcode, salt, unlock string) string {
	t.Helper()
	path := filepath.Join(dir, TripcodeFileName)
	if err := saveTripcodeFile(path, tripcode, salt, unlock); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadTripcodeFileRoundtrip(t *testing.T) {
	dir := t.TempDir()
	path := writeTripcodeFixture(t, dir, "s3cret-trip", "my-salt", "unlock-1")
	t.Setenv("V2V_PASSPHRASE", "wrong-unlock")
	if _, _, _, err := loadTripcodeFile(path); err == nil {
		t.Fatal("wrong unlock must fail")
	}
	t.Setenv("V2V_PASSPHRASE", "unlock-1")
	tc, salt, found, err := loadTripcodeFile(path)
	if err != nil || !found || tc != "s3cret-trip" || salt != "my-salt" {
		t.Fatalf("got %q %q %v %v", tc, salt, found, err)
	}
}

func TestLoadTripcodeFileOldWithoutSalt(t *testing.T) {
	dir := t.TempDir()
	path := writeTripcodeFixture(t, dir, "s3cret-trip", "", "unlock-1")
	t.Setenv("V2V_PASSPHRASE", "unlock-1")
	tc, salt, found, err := loadTripcodeFile(path)
	if err != nil || !found || tc != "s3cret-trip" || salt != "" {
		t.Fatalf("old file must decode with empty salt: %q %q %v %v", tc, salt, found, err)
	}
}

func TestLoadTripcodeFileRefusesPlaintext(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, TripcodeFileName)
	if err := os.WriteFile(path, []byte(`{"version":3,"tripcode":"plain"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := loadTripcodeFile(path); err == nil {
		t.Fatal("plaintext file must be refused")
	}
}

func TestLoadTripcodeFileMissing(t *testing.T) {
	_, _, found, err := loadTripcodeFile(filepath.Join(t.TempDir(), TripcodeFileName))
	if err != nil || found {
		t.Fatalf("got found=%v err=%v", found, err)
	}
}

func TestSaveTripcodeFileNeedsUnlock(t *testing.T) {
	path := filepath.Join(t.TempDir(), TripcodeFileName)
	if err := saveTripcodeFile(path, "s3cret", "", ""); err == nil {
		t.Fatal("empty unlock must refuse (never plaintext)")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("refused save must not create a file")
	}
}

func TestOfferTripcodeSave(t *testing.T) {
	if !offerTripcodeSave(strings.NewReader("y\n")) {
		t.Fatal("y must save")
	}
	if !offerTripcodeSave(strings.NewReader("YES\n")) {
		t.Fatal("YES must save")
	}
	for _, in := range []string{"\n", "n\n", "no\n", "xyz\n", ""} {
		if offerTripcodeSave(strings.NewReader(in)) {
			t.Fatalf("%q must not save (default No)", in)
		}
	}
}

func TestResolveTripcodeEnvPrecedence(t *testing.T) {
	t.Setenv("V2V_TRIPCODE", "from-env")
	t.Setenv("V2V_TRIP_SALT", "env-salt")
	tc, salt, src, err := resolveTripcode(true, t.TempDir(), "u", "h")
	if err != nil || tc != "from-env" || salt != "env-salt" || src != "env V2V_TRIP_SALT" {
		t.Fatalf("got %q %q %q %v", tc, salt, src, err)
	}
}

func TestResolveTripcodeNoFlag(t *testing.T) {
	tc, salt, src, err := resolveTripcode(false, t.TempDir(), "u", "h")
	if err != nil || tc != "" || salt != "" || src != "" {
		t.Fatalf("got %q %q %q %v", tc, salt, src, err)
	}
}

func TestResolveTripcodeFromFile(t *testing.T) {
	dir := t.TempDir()
	writeTripcodeFixture(t, dir, "from-file", "file-salt", "unlock-9")
	t.Setenv("V2V_TRIPCODE", "")
	t.Setenv("V2V_PASSPHRASE", "unlock-9")
	t.Setenv("V2V_TRIP_SALT", "")
	tc, salt, src, err := resolveTripcode(true, dir, "u", "h")
	if err != nil || tc != "from-file" || salt != "file-salt" || src != "tripcode.json" {
		t.Fatalf("got %q %q %q %v", tc, salt, src, err)
	}
}

func TestResolveTripcodeSaltPrecedence(t *testing.T) {
	dir := t.TempDir()
	writeTripcodeFixture(t, dir, "from-file", "file-salt", "unlock-9")
	t.Setenv("V2V_TRIPCODE", "")
	t.Setenv("V2V_PASSPHRASE", "unlock-9")
	// Env beats file.
	t.Setenv("V2V_TRIP_SALT", "env-salt")
	if _, salt, src, err := resolveTripcode(true, dir, "u", "h"); err != nil || salt != "env-salt" || src != "env V2V_TRIP_SALT" {
		t.Fatalf("env must beat file salt: %q %q %v", salt, src, err)
	}
	// File beats config.
	t.Setenv("V2V_TRIP_SALT", "")
	old := ClientCfg
	ClientCfg = config.DefaultClientConfig()
	ClientCfg.Crypto.Salt = "config-salt"
	t.Cleanup(func() { ClientCfg = old })
	if _, salt, src, err := resolveTripcode(true, dir, "u", "h"); err != nil || salt != "file-salt" || src != "tripcode.json" {
		t.Fatalf("file must beat config salt: %q %q %v", salt, src, err)
	}
	// Config beats server default (env passphrase skips the file, so
	// the salt resolves env > config > server default).
	t.Setenv("V2V_TRIPCODE", "some-pass")
	if _, salt, src, err := resolveTripcode(true, dir, "u", "h"); err != nil || salt != "config-salt" || src != "config crypto.salt" {
		t.Fatalf("config must beat server default: %q %q %v", salt, src, err)
	}
}

// withPipedStdin swaps os.Stdin for a pipe feeding input. passprompt and
// tui detect the pipe as non-TTY and take the plain-line fallbacks.
func withPipedStdin(t *testing.T, input string) {
	t.Helper()
	old := os.Stdin
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.WriteString(input); err != nil {
		t.Fatal(err)
	}
	_ = w.Close()
	os.Stdin = r
	t.Cleanup(func() {
		os.Stdin = old
		_ = r.Close()
	})
}

// Weak-gate behavior on the piped path: a weak tripcode warns and asks;
// "n" aborts, "y" proceeds without saving.
func TestResolveTripcode_WeakGate(t *testing.T) {
	dir := t.TempDir()
	// Abort path: weak tc, matching re-entry, "n" at the weak gate.
	withPipedStdin(t, "password123\npassword123\nn\n")
	if _, _, _, err := resolveTripcode(true, dir, "Alice", "example.com"); err == nil {
		t.Fatal("weak tripcode without confirm must abort")
	}
	// Accept path: weak tc, "y" at gate, "n" at save offer.
	withPipedStdin(t, "password123\npassword123\ny\nn\n")
	tc, _, _, err := resolveTripcode(true, dir, "Alice", "example.com")
	if err != nil || tc != "password123" {
		t.Fatalf("weak tripcode with confirm: tc=%q err=%v", tc, err)
	}
	// Mismatch path: re-entry differs, three rounds then error.
	withPipedStdin(t, "aaa\naab\naaa\naab\naaa\naab\n")
	if _, _, _, err := resolveTripcode(true, dir, "Alice", "example.com"); err == nil {
		t.Fatal("mismatched re-entry must fail after 3 rounds")
	}
}

// TestResolveTripcode_EnvWarnOnly: env secrets warn but never refuse.
func TestResolveTripcode_EnvWarnOnly(t *testing.T) {
	t.Setenv("V2V_TRIPCODE", "password123")
	tc, _, _, err := resolveTripcode(true, t.TempDir(), "Alice", "example.com")
	if err != nil || tc != "password123" {
		t.Fatalf("env tripcode must pass with warning only: %q %v", tc, err)
	}
}

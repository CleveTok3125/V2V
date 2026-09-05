package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTripcodeFixture(t *testing.T, dir, tripcode, unlock string) string {
	t.Helper()
	path := filepath.Join(dir, TripcodeFileName)
	if err := saveTripcodeFile(path, tripcode, unlock); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadTripcodeFileRoundtrip(t *testing.T) {
	dir := t.TempDir()
	path := writeTripcodeFixture(t, dir, "s3cret-trip", "unlock-1")
	t.Setenv("V2V_PASSPHRASE", "wrong-unlock")
	if _, _, err := loadTripcodeFile(path); err == nil {
		t.Fatal("wrong unlock must fail")
	}
	t.Setenv("V2V_PASSPHRASE", "unlock-1")
	tc, found, err := loadTripcodeFile(path)
	if err != nil || !found || tc != "s3cret-trip" {
		t.Fatalf("got %q %v %v", tc, found, err)
	}
}

func TestLoadTripcodeFileRefusesPlaintext(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, TripcodeFileName)
	if err := os.WriteFile(path, []byte(`{"version":3,"tripcode":"plain"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := loadTripcodeFile(path); err == nil {
		t.Fatal("plaintext file must be refused")
	}
}

func TestLoadTripcodeFileMissing(t *testing.T) {
	_, found, err := loadTripcodeFile(filepath.Join(t.TempDir(), TripcodeFileName))
	if err != nil || found {
		t.Fatalf("got found=%v err=%v", found, err)
	}
}

func TestSaveTripcodeFileNeedsUnlock(t *testing.T) {
	path := filepath.Join(t.TempDir(), TripcodeFileName)
	if err := saveTripcodeFile(path, "s3cret", ""); err == nil {
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
	tc, err := resolveTripcode(true, t.TempDir())
	if err != nil || tc != "from-env" {
		t.Fatalf("got %q %v", tc, err)
	}
}

func TestResolveTripcodeNoFlag(t *testing.T) {
	tc, err := resolveTripcode(false, t.TempDir())
	if err != nil || tc != "" {
		t.Fatalf("got %q %v", tc, err)
	}
}

func TestResolveTripcodeFromFile(t *testing.T) {
	dir := t.TempDir()
	writeTripcodeFixture(t, dir, "from-file", "unlock-9")
	t.Setenv("V2V_TRIPCODE", "")
	t.Setenv("V2V_PASSPHRASE", "unlock-9")
	tc, err := resolveTripcode(true, dir)
	if err != nil || tc != "from-file" {
		t.Fatalf("got %q %v", tc, err)
	}
}

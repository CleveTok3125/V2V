package clientcore

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/CleveTok3125/V2V/lib/identity"
)

// stubPrompter answers with a canned secret and records that it was asked.
type stubPrompter struct {
	secret string
	err    error
	reason SecretReason
	asked  int
}

func (s *stubPrompter) Secret(reason SecretReason) (string, error) {
	s.asked++
	s.reason = reason
	return s.secret, s.err
}

func writePlain(t *testing.T, path string) {
	t.Helper()
	f := &identity.IdentityFile{Ed25519: &identity.Ed25519Identity{Role: "admin", PrivateKey: "aa", HmacShield: "bb"}}
	if err := f.Save(path); err != nil {
		t.Fatal(err)
	}
}

func writeEncrypted(t *testing.T, path, pass string) {
	t.Helper()
	f := &identity.IdentityFile{Ed25519: &identity.Ed25519Identity{Role: "admin", PrivateKey: "aa", HmacShield: "bb"}}
	if err := f.SaveEncrypted(path, []byte(pass), nil); err != nil {
		t.Fatal(err)
	}
}

func TestLoadIdentityFilePlaintextAsksNothing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "key.json")
	writePlain(t, path)
	p := &stubPrompter{}
	warned := 0
	idf, pw, err := LoadIdentityFile(path, p, func(string) { warned++ })
	if err != nil {
		t.Fatal(err)
	}
	if idf == nil || idf.Ed25519 == nil {
		t.Fatal("plaintext identity not loaded")
	}
	if pw != nil {
		t.Fatalf("plaintext load must return no passphrase, got %q", pw)
	}
	if p.asked != 0 || warned != 0 {
		t.Fatalf("plaintext load must not prompt (asked=%d) or warn (warned=%d)", p.asked, warned)
	}
}

func TestLoadIdentityFileEnvPassphraseWarnsAndReturnsIt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "key.json")
	writeEncrypted(t, path, "unlock")
	t.Setenv("V2V_PASSPHRASE", "unlock")

	p := &stubPrompter{}
	var warnedWith string
	idf, pw, err := LoadIdentityFile(path, p, func(pass string) { warnedWith = pass })
	if err != nil {
		t.Fatal(err)
	}
	if idf == nil || idf.Ed25519 == nil {
		t.Fatal("encrypted identity not unlocked")
	}
	if string(pw) != "unlock" {
		t.Fatalf("returned passphrase = %q, want the one that unlocked", pw)
	}
	if warnedWith != "unlock" {
		t.Fatalf("warn got %q, want the env passphrase so the caller can rate it", warnedWith)
	}
	if p.asked != 0 {
		t.Fatal("the env passphrase must win over the prompt")
	}
	identity.ZeroBytes(pw)
}

func TestLoadIdentityFilePromptsWhenNoEnvPassphrase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "key.json")
	writeEncrypted(t, path, "unlock")
	t.Setenv("V2V_PASSPHRASE", "")

	p := &stubPrompter{secret: "unlock"}
	idf, pw, err := LoadIdentityFile(path, p, nil)
	if err != nil {
		t.Fatal(err)
	}
	if idf == nil || idf.Ed25519 == nil {
		t.Fatal("prompted passphrase did not unlock the file")
	}
	if string(pw) != "unlock" {
		t.Fatalf("returned passphrase = %q, want the prompted one", pw)
	}
	if p.asked != 1 || p.reason != ReasonKeyFilePassphrase {
		t.Fatalf("prompter asked=%d reason=%v, want one key-file passphrase", p.asked, p.reason)
	}
	identity.ZeroBytes(pw)
}

func TestLoadIdentityFileWrongPassphraseReturnsNoSecret(t *testing.T) {
	path := filepath.Join(t.TempDir(), "key.json")
	writeEncrypted(t, path, "unlock")
	t.Setenv("V2V_PASSPHRASE", "")

	p := &stubPrompter{secret: "wrong"}
	idf, pw, err := LoadIdentityFile(path, p, nil)
	if err == nil {
		t.Fatal("a wrong passphrase must fail")
	}
	if idf != nil || pw != nil {
		t.Fatal("a failed unlock must return neither identity nor passphrase")
	}
}

func TestLoadIdentityFilePromptErrorPropagates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "key.json")
	writeEncrypted(t, path, "unlock")
	t.Setenv("V2V_PASSPHRASE", "")

	boom := errors.New("no terminal")
	p := &stubPrompter{err: boom}
	if _, pw, err := LoadIdentityFile(path, p, nil); !errors.Is(err, boom) || pw != nil {
		t.Fatalf("prompt error = %v (pw %q), want it propagated with no secret", err, pw)
	}
}

// A missing file is not an unlock: the caller sees the read error, not a
// silent prompt.
func TestLoadIdentityFileMissingFileFails(t *testing.T) {
	path := filepath.Join(t.TempDir(), "absent.json")
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("fixture exists: %v", err)
	}
	p := &stubPrompter{secret: "unlock"}
	if _, _, err := LoadIdentityFile(path, p, nil); err == nil {
		t.Fatal("a missing key file must fail")
	}
	if p.asked != 0 {
		t.Fatal("a missing file must not prompt")
	}
}

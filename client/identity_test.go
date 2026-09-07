package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CleveTok3125/V2V/identity"
)

func TestPickIdentityFrom(t *testing.T) {
	pick := func(line string, eof bool) (bool, bool) {
		return pickIdentityFrom(strings.NewReader(line+"\n"), &IdentityFile{
			Version: identity.Version,
			Ed25519: &Ed25519Identity{}, Passkey: &PasskeyIdentity{},
		})
	}
	// EOF default prefers passkey
	if ed, pk := pick("", true); ed || !pk {
		t.Errorf("EOF default should prefer passkey, got ed=%v pk=%v", ed, pk)
	}
	if ed, pk := pick("1", false); !ed || pk {
		t.Errorf("menu '1' should pick ed25519, got ed=%v pk=%v", ed, pk)
	}
	if _, pk := pick("2", false); !pk {
		t.Errorf("menu '2' should pick passkey")
	}
}

// TestEncryptedCounterSaveStaysEncrypted: loading a passphrase-encrypted
// file then saving (passkey counter persist) must keep the file
// encrypted with the same passphrase — never silently decrypt to disk.
func TestEncryptedCounterSaveStaysEncrypted(t *testing.T) {
	path := filepath.Join(t.TempDir(), "key.json")
	f := &IdentityFile{Ed25519: &Ed25519Identity{Role: "admin", PrivateKey: "aa", HmacShield: "bb"}}
	if err := f.SaveEncrypted(path, []byte("unlock"), nil); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(ClearLoadedPassphrase)
	t.Setenv("V2V_PASSPHRASE", "unlock")
	idf, err := LoadIdentityFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !loadedWasEncrypted || len(loadedPassphrase) == 0 {
		t.Fatal("unlock secret not remembered after load")
	}
	if err := SaveIdentityFileEncrypted(path, idf); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(path)
	var probe map[string]any
	if err := json.Unmarshal(raw, &probe); err == nil {
		if _, hasRole := probe["ed25519"]; hasRole {
			t.Fatal("encrypted key file was rewritten as plaintext")
		}
	}
	if _, err := identity.LoadEncrypted(path, []byte("unlock")); err != nil {
		t.Fatalf("saved file no longer opens with the passphrase: %v", err)
	}
	ClearLoadedPassphrase()
	if loadedWasEncrypted || loadedPassphrase != nil {
		t.Fatal("ClearLoadedPassphrase did not wipe state")
	}
}

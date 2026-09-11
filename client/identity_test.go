package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/CleveTok3125/V2V/internal/identity"
)

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

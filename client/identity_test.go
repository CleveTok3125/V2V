package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestIdentityFileRoundtrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "key.json")

	f := &IdentityFile{
		Ed25519: &Ed25519Identity{Role: "admin", PrivateKey: "aa", HmacShield: "bb"},
		Passkey: &PasskeyIdentity{Role: "member", CredentialID: "cid", PrivateKey: "pk",
			PublicKey: "cose", RPID: "r", Origin: "o", SignCount: 7},
	}
	if err := f.Save(path); err != nil {
		t.Fatal(err)
	}
	got, err := LoadIdentityFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Ed25519 == nil || got.Ed25519.Role != "admin" ||
		got.Passkey == nil || got.Passkey.SignCount != 7 || got.Version != identityFileVersion {
		t.Fatalf("roundtrip mismatch: %+v", got)
	}
}

func TestLegacyFlatKeyJSONLoads(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "key.json")
	legacy := `{"role":"admin","private_key":"aabb","hmac_shield":"ccdd"}`
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := LoadIdentityFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Ed25519 == nil || got.Ed25519.Role != "admin" || got.Passkey != nil {
		t.Fatalf("legacy wrap mismatch: %+v", got)
	}
}

func TestGeneratePreservesSiblingSlot(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "key.json")
	idf := &IdentityFile{Version: identityFileVersion,
		Passkey: &PasskeyIdentity{Role: "member", CredentialID: "keep-me"}}
	if err := idf.Save(path); err != nil {
		t.Fatal(err)
	}

	// Simulate the ed25519 generator: it only sets its own slot.
	reloaded, _ := LoadIdentityFile(path)
	reloaded.Ed25519 = &Ed25519Identity{Role: "admin", PrivateKey: "xx", HmacShield: "yy"}
	if err := reloaded.Save(path); err != nil {
		t.Fatal(err)
	}

	final, _ := LoadIdentityFile(path)
	if final.Passkey == nil || final.Passkey.CredentialID != "keep-me" {
		t.Fatal("sibling passkey slot was destroyed")
	}
	if final.Ed25519 == nil {
		t.Fatal("ed25519 slot missing")
	}
}

func TestMergeRolesFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "roles.json")
	existing := `{"other": {"can_message_unlimited": false}}`
	if err := os.WriteFile(path, []byte(existing), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := mergeRolesFile(path, "member", func(e map[string]any) {
		e["identities"] = []map[string]string{{"public_key": "P", "hmac_shield": "H"}}
		e["can_message_unlimited"] = true
	}); err != nil {
		t.Fatal(err)
	}
	// second merge: append a passkey credential, dedupe on repeat
	addCred := func(cred string) error {
		return mergeRolesFile(path, "member", func(e map[string]any) {
			list, _ := e["passkeys"].([]any)
			entry := map[string]any{"credential_id": cred}
			for i, raw := range list {
				if ex, _ := raw.(map[string]any); ex != nil && ex["credential_id"] == cred {
					list[i] = entry
					return
				}
			}
			e["passkeys"] = append(list, entry)
		})
	}
	if err := addCred("c1"); err != nil {
		t.Fatal(err)
	}
	if err := addCred("c2"); err != nil {
		t.Fatal(err)
	}
	if err := addCred("c2"); err != nil { // dedupe
		t.Fatal(err)
	}

	data, _ := os.ReadFile(path)
	var root map[string]any
	if json.Unmarshal(data, &root) != nil {
		t.Fatal("merged roles.json invalid")
	}
	member := root["member"].(map[string]any)
	other := root["other"].(map[string]any)
	if len(member["passkeys"].([]any)) != 2 {
		t.Errorf("expected 2 deduped credentials, got %v", member["passkeys"])
	}
	if _, hasID := member["identities"]; !hasID {
		t.Error("identities lost after passkey merge")
	}
	if other["can_message_unlimited"] != false {
		t.Error("sibling role was modified")
	}
}

func TestMergeRolesFileCorruptAborts(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "roles.json")
	os.WriteFile(path, []byte("{not json"), 0o600)
	err := mergeRolesFile(path, "x", func(e map[string]any) {})
	if err == nil || !strings.Contains(err.Error(), "không tự ghi đè") {
		t.Fatalf("corrupt roles.json must abort, got: %v", err)
	}
}

func TestPickIdentityFrom(t *testing.T) {
	f := func(line string, eof bool) *strings.Reader {
		if eof {
			return strings.NewReader("")
		}
		return strings.NewReader(line + "\n")
	}
	useEd, usePk := pickIdentityFrom(strings.NewReader(""), &IdentityFile{
		Version: identityFileVersion,
		Ed25519: &Ed25519Identity{Role: "a"}, Passkey: &PasskeyIdentity{Role: "b"},
	})
	if useEd || !usePk {
		t.Errorf("EOF default should prefer passkey, got ed=%v pk=%v", useEd, usePk)
	}
	useEd, usePk = pickIdentityFrom(f("1", false), &IdentityFile{
		Version: identityFileVersion,
		Ed25519: &Ed25519Identity{}, Passkey: &PasskeyIdentity{},
	})
	if !useEd || usePk {
		t.Errorf("menu '1' should pick ed25519, got ed=%v pk=%v", useEd, usePk)
	}
}

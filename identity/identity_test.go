package identity

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestIdentityRoundtrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "key.json")
	f := &IdentityFile{
		Ed25519: &Ed25519Identity{Role: "admin", PrivateKey: "aa", HmacShield: "bb"},
		Passkey: &PasskeyIdentity{Role: "member", CredentialID: "cid", PrivateKey: "pk",
			PublicKey: "cose", SignCount: 7},
	}
	if err := f.Save(path); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Ed25519 == nil || got.Ed25519.Role != "admin" ||
		got.Passkey == nil || got.Passkey.SignCount != 7 || got.Version != Version {
		t.Fatalf("roundtrip mismatch: %+v", got)
	}
}

func TestLegacyFlatKeyJSONLoads(t *testing.T) {
	path := filepath.Join(t.TempDir(), "key.json")
	if err := os.WriteFile(path,
		[]byte(`{"role":"admin","private_key":"aabb","hmac_shield":"ccdd"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Ed25519 == nil || got.Ed25519.Role != "admin" || got.Passkey != nil {
		t.Fatalf("legacy wrap mismatch: %+v", got)
	}
}

func TestGeneratePreservesSiblingSlot(t *testing.T) {
	path := filepath.Join(t.TempDir(), "key.json")
	f := &IdentityFile{Version: Version,
		Passkey: &PasskeyIdentity{Role: "member", CredentialID: "keep-me"}}
	if err := f.Save(path); err != nil {
		t.Fatal(err)
	}
	reloaded, _ := Load(path)
	reloaded.Ed25519 = &Ed25519Identity{Role: "admin", PrivateKey: "xx", HmacShield: "yy"}
	if err := reloaded.Save(path); err != nil {
		t.Fatal(err)
	}
	final, _ := Load(path)
	if final.Passkey == nil || final.Passkey.CredentialID != "keep-me" || final.Ed25519 == nil {
		t.Fatal("sibling slot destroyed")
	}
}

func TestMergeRolesFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "roles.json")
	os.WriteFile(path, []byte(`{"other": {"can_message_unlimited": false}}`), 0o600)

	if err := MergeRolesFile(path, "member", func(e map[string]any) {
		e["identities"] = []map[string]string{{"public_key": "P", "hmac_shield": "H"}}
		e["can_message_unlimited"] = true
	}); err != nil {
		t.Fatal(err)
	}
	addCred := func(cred string) error {
		return MergeRolesFile(path, "member", func(e map[string]any) {
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
	for _, c := range []string{"c1", "c2", "c2"} {
		if err := addCred(c); err != nil {
			t.Fatal(err)
		}
	}
	data, _ := os.ReadFile(path)
	var root map[string]any
	if json.Unmarshal(data, &root) != nil {
		t.Fatal("merged roles.json invalid")
	}
	member := root["member"].(map[string]any)
	other := root["other"].(map[string]any)
	if len(member["passkeys"].([]any)) != 2 {
		t.Errorf("dedupe failed: %v", member["passkeys"])
	}
	if _, ok := member["identities"]; !ok {
		t.Error("identities lost")
	}
	if other["can_message_unlimited"] != false {
		t.Error("sibling role modified")
	}
}

func TestMergeRolesFileCorruptAborts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "roles.json")
	os.WriteFile(path, []byte("{not json"), 0o600)
	err := MergeRolesFile(path, "x", func(e map[string]any) {})
	if err == nil || !contains(err.Error(), "refusing to overwrite") && !contains(err.Error(), "không tự ghi đè") {
		t.Fatalf("corrupt roles.json must abort, got: %v", err)
	}
}

func contains(s, sub string) bool { return len(s) >= len(sub) && strings.Contains(s, sub) }

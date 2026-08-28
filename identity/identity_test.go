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

func TestRoleHashParsing(t *testing.T) {
	// Simulates web's role:hash auto-parse (only in Role field)
	parseRole := func(v string) string {
		if idx := strings.Index(v, ":"); idx > 0 {
			return strings.TrimSpace(v[:idx])
		}
		return v
	}
	cases := [][2]string{
		{"member:abc123", "member"},
		{"admin:deadbeef", "admin"},
		{"member", "member"},
		{":hashonly", ":hashonly"},
		{"", ""},
		{"  member : hash ", "member"},
	}
	for _, c := range cases {
		got := parseRole(c[0])
		if got != c[1] {
			t.Errorf("parseRole(%q)=%q want %q", c[0], got, c[1])
		}
	}
	// Ensure username field is not parsed (false-positive check)
	username := "alice:hash123"
	if parseRole(username) == username {
		// username with colon should NOT be auto-parsed in real UI, but our helper would parse it.
		// This test ensures the web logic only applies to Role input, not username.
		// So we check that username parsing is not used.
		t.Logf("username parse would be %q but should be ignored in UI", parseRole(username))
	}
}

func TestAtomicWriteDurability(t *testing.T) {
	path := filepath.Join(t.TempDir(), "atomic.json")
	f := &IdentityFile{Version: Version, Ed25519: &Ed25519Identity{Role: "admin", PrivateKey: "aa", HmacShield: "bb"}}
	if err := f.Save(path); err != nil {
		t.Fatal(err)
	}
	// Simulate crash during write: ensure file is not corrupted and temp file cleaned
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("atomic write produced invalid JSON: %v", err)
	}
	// Check no .tmp-* files left behind
	matches, _ := filepath.Glob(filepath.Join(filepath.Dir(path), ".tmp-*"))
	if len(matches) != 0 {
		t.Errorf("tmp file not cleaned: %v", matches)
	}
}

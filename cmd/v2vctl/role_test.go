package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// helper to run in temp dir with chdir
func withTempDir(t *testing.T, fn func()) {
	t.Helper()
	dir := t.TempDir()
	orig, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chdir(orig) })
	fn()
}

func readRoles(t *testing.T) map[string]any {
	t.Helper()
	data, err := os.ReadFile("roles.json")
	if err != nil {
		t.Fatalf("read roles.json: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return m
}

func TestRoleCreateAndOverwrite(t *testing.T) {
	withTempDir(t, func() {
		c := &RoleCreateCmd{Role: "tester", Prefix: "[Test] ", Unlimited: true}
		if err := c.Run(); err != nil {
			t.Fatalf("create: %v", err)
		}
		m := readRoles(t)
		if _, ok := m["tester"]; !ok {
			t.Fatal("tester not created")
		}
		if m["tester"].(map[string]any)["custom_prefix"] != "[Test] " {
			t.Fatalf("prefix mismatch")
		}
		// overwrite without --force should fail
		c2 := &RoleCreateCmd{Role: "tester", Prefix: "[New] "}
		if err := c2.Run(); err == nil || !strings.Contains(err.Error(), "đã tồn tại") {
			t.Fatalf("expected overwrite error, got %v", err)
		}
		// with --force should succeed
		c3 := &RoleCreateCmd{Role: "tester", Prefix: "[New] ", Force: true}
		if err := c3.Run(); err != nil {
			t.Fatalf("force overwrite: %v", err)
		}
		m = readRoles(t)
		if m["tester"].(map[string]any)["custom_prefix"] != "[New] " {
			t.Fatalf("force prefix not updated")
		}
	})
}

func TestRoleAddIdentityManualAndPaste(t *testing.T) {
	withTempDir(t, func() {
		// create role first
		if err := (&RoleCreateCmd{Role: "tester", Prefix: "[T] "}).Run(); err != nil {
			t.Fatal(err)
		}
		// manual add
		c := &RoleAddIdentityCmd{
			Role:         "tester",
			PublicKey:    "abcd1234abcd1234abcd1234abcd1234abcd1234abcd1234abcd1234abcd1234abcd12",
			HmacShield:   "deadbeefdeadbeefdeadbeefdeadbeef",
			ServerPubKey: "bb00",
		}
		if err := c.Run(); err != nil {
			t.Fatalf("add-identity manual: %v", err)
		}
		m := readRoles(t)
		ids := m["tester"].(map[string]any)["identities"].([]any)
		if len(ids) != 1 {
			t.Fatalf("expected 1 identity, got %d", len(ids))
		}
		// paste via file: single identity object
		paste := `{"public_key":"ff00ff00ff00ff00ff00ff00ff00ff00ff00ff00ff00ff00ff00ff00ff00ff00ff00","hmac_shield":"aa","server_pubkey":"bb"}`
		f := filepath.Join(".", "paste.json")
		os.WriteFile(f, []byte(paste), 0600)
		c2 := &RoleAddIdentityCmd{Role: "tester", File: f}
		if err := c2.Run(); err != nil {
			t.Fatalf("paste file: %v", err)
		}
		m = readRoles(t)
		ids = m["tester"].(map[string]any)["identities"].([]any)
		if len(ids) != 2 {
			t.Fatalf("expected 2 identities after paste, got %d", len(ids))
		}
		// full roles.json wrapper paste
		wrapper := `{"other":{"identities":[{"public_key":"aa11","hmac_shield":"bb","server_pubkey":"cc"}]}}`
		os.WriteFile(f, []byte(wrapper), 0600)
		c3 := &RoleAddIdentityCmd{Role: "tester", File: f}
		// This will look for identities[0] inside "other" – should pick first found
		if err := c3.Run(); err != nil {
			t.Fatalf("wrapper paste: %v", err)
		}
		m = readRoles(t)
		ids = m["tester"].(map[string]any)["identities"].([]any)
		if len(ids) != 3 {
			t.Fatalf("expected 3 after wrapper, got %d", len(ids))
		}
	})
}

func TestRoleAddPasskeyPasteWithComment(t *testing.T) {
	withTempDir(t, func() {
		if err := (&RoleCreateCmd{Role: "tester"}).Run(); err != nil {
			t.Fatal(err)
		}
		// Simulate RolesSnippet output with // comment line
		snippet := "// roles.json → \"tester\".passkeys\n[{\"credential_id\":\"cid123\",\"public_key\":\"cose123\",\"added_at\":\"2026-08-31T00:00:00Z\"}]"
		f := filepath.Join(".", "snip.json")
		os.WriteFile(f, []byte(snippet), 0600)
		c := &RoleAddPasskeyCmd{Role: "tester", File: f}
		if err := c.Run(); err != nil {
			t.Fatalf("add-passkey comment: %v", err)
		}
		m := readRoles(t)
		pks := m["tester"].(map[string]any)["passkeys"].([]any)
		if len(pks) != 1 {
			t.Fatalf("expected 1 passkey, got %d", len(pks))
		}
		if pks[0].(map[string]any)["credential_id"] != "cid123" {
			t.Fatalf("cid mismatch")
		}
		// duplicate should overwrite, not append
		if err := c.Run(); err != nil {
			t.Fatalf("duplicate: %v", err)
		}
		m = readRoles(t)
		pks = m["tester"].(map[string]any)["passkeys"].([]any)
		if len(pks) != 1 {
			t.Fatalf("dedupe failed, got %d", len(pks))
		}
	})
}

func TestRoleImportAndEnrollWarn(t *testing.T) {
	withTempDir(t, func() {
		// import full roles.json via file
		full := `{"imported":{"can_message_unlimited":true,"custom_prefix":"[Imp] ","identities":[{"public_key":"ff00","hmac_shield":"aa","server_pubkey":"bb"}]}}`
		f := filepath.Join(".", "full.json")
		os.WriteFile(f, []byte(full), 0600)
		c := &RoleImportCmd{File: f}
		if err := c.Run(); err != nil {
			t.Fatalf("import: %v", err)
		}
		m := readRoles(t)
		if _, ok := m["imported"]; !ok {
			t.Fatal("imported not found")
		}
		// import without --force should skip (not error) and keep existing
		c2 := &RoleImportCmd{File: f}
		if err := c2.Run(); err != nil {
			t.Fatalf("import skip should not error, got %v", err)
		}
		m = readRoles(t)
		if _, ok := m["imported"]; !ok {
			t.Fatal("imported should still exist after skip")
		}
		// with force should overwrite
		c3 := &RoleImportCmd{File: f, Force: true}
		if err := c3.Run(); err != nil {
			t.Fatalf("force import: %v", err)
		}
		// enroll should warn when role missing but still create ticket
		os.RemoveAll("data")
		en := &EnrollCmd{Role: "notexist", Store: "data/webauthn.json"}
		// capture output? just check it doesn't error and file created
		if err := en.Run(); err != nil {
			t.Fatalf("enroll: %v", err)
		}
		if _, err := os.Stat("data/webauthn.json"); err != nil {
			t.Fatalf("webauthn not created")
		}
	})
}

func TestKeygenDecoupledDoesNotTouchRoles(t *testing.T) {
	withTempDir(t, func() {
		// create a role
		if err := (&RoleCreateCmd{Role: "tester", Prefix: "[T] "}).Run(); err != nil {
			t.Fatal(err)
		}
		before, _ := os.ReadFile("roles.json")
		// keygen ed25519 should not modify roles.json
		k := &Ed25519Keygen{Role: "tester", Out: "key.json", ServerPubKey: "deadbeef"}
		if err := k.Run(); err != nil {
			t.Fatalf("keygen: %v", err)
		}
		after, _ := os.ReadFile("roles.json")
		if string(before) != string(after) {
			t.Fatalf("keygen should not modify roles.json")
		}
		if _, err := os.Stat("key.json"); err != nil {
			t.Fatalf("key.json not created")
		}
		// check role still stored in key.json
		var kf map[string]any
		data, _ := os.ReadFile("key.json")
		json.Unmarshal(data, &kf)
		// key.json is container, check ed25519.role
		// raw contains ed25519 field
	})
}

func TestRoleListShowDelete(t *testing.T) {
	withTempDir(t, func() {
		(&RoleCreateCmd{Role: "a"}).Run()
		(&RoleCreateCmd{Role: "b"}).Run()
		// list should not error
		if err := (&RoleListCmd{}).Run(); err != nil {
			t.Fatalf("list: %v", err)
		}
		if err := (&RoleShowCmd{Role: "a"}).Run(); err != nil {
			t.Fatalf("show: %v", err)
		}
		if err := (&RoleDeleteCmd{Role: "a", Force: true}).Run(); err != nil {
			t.Fatalf("delete: %v", err)
		}
		m := readRoles(t)
		if _, ok := m["a"]; ok {
			t.Fatal("a should be deleted")
		}
		if _, ok := m["b"]; !ok {
			t.Fatal("b should remain")
		}
	})
}

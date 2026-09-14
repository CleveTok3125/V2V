package main

import (
	"os"
	"path/filepath"
	"testing"
)

// Booting without roles must fail closed instead of silently
// granting default permissions to everyone.
func TestLoadRolesFailsClosed(t *testing.T) {
	old := RolesFilePaths
	t.Cleanup(func() { RolesFilePaths = old })
	path := filepath.Join(t.TempDir(), "roles.json")
	RolesFilePaths = []string{path}

	s := NewChatServer()
	if err := s.LoadRoles(); err == nil {
		t.Fatal("missing roles must fail closed")
	}
	if err := os.WriteFile(path, []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := s.LoadRoles(); err == nil {
		t.Fatal("corrupt roles must fail")
	}
	if err := os.WriteFile(path, []byte(`{"admin":{"can_message_unlimited":true}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := s.LoadRoles(); err != nil {
		t.Fatalf("valid roles must load: %v", err)
	}
	if _, ok := s.RoleRegistry["admin"]; !ok {
		t.Fatal("admin role missing after load")
	}
}

package main

import (
	"path/filepath"
	"testing"
)

func withServerRoot(t *testing.T, root string) {
	t.Helper()
	oldRoot, oldEnv, oldRoles := ServerRoot, EnvFilePaths, RolesFilePaths
	InitServerPaths(root)
	t.Cleanup(func() { ServerRoot, EnvFilePaths, RolesFilePaths = oldRoot, oldEnv, oldRoles })
}

func TestResolveUnderRoot(t *testing.T) {
	if got := resolveUnderRoot("instances/prod", "./data/app.log"); got != filepath.Join("instances/prod", "data/app.log") {
		t.Fatalf("relative must join root, got %q", got)
	}
	if got := resolveUnderRoot("instances/prod", "/srv/app.log"); got != "/srv/app.log" {
		t.Fatalf("absolute must stay, got %q", got)
	}
	if got := resolveUnderRoot("instances/prod", "  "); got != "" {
		t.Fatalf("blank must stay blank, got %q", got)
	}
}

func TestDataPathRootRelativeOverride(t *testing.T) {
	withServerRoot(t, "instances/prod")
	t.Setenv("DATA_DIR", "./data")
	if got := dataPath("app.log"); got != filepath.Join("instances/prod", "data", "app.log") {
		t.Fatalf("relative DATA_DIR must anchor at root, got %q", got)
	}
}

func TestDefaultServerRoot(t *testing.T) {
	t.Setenv("V2V_ROOT", "")
	if got := DefaultServerRoot(); got != filepath.Join("instances", "default") {
		t.Fatalf("default root = %q", got)
	}
	t.Setenv("V2V_ROOT", "instances/prod")
	if got := DefaultServerRoot(); got != "instances/prod" {
		t.Fatalf("env root = %q", got)
	}
}

func TestInitServerPaths(t *testing.T) {
	withServerRoot(t, "instances/prod")
	if EnvFilePaths[0] != filepath.Join("instances/prod", ".env") {
		t.Fatalf("env first = %q", EnvFilePaths[0])
	}
	if RolesFilePaths[0] != filepath.Join("instances/prod", "config", "roles.json") {
		t.Fatalf("roles first = %q", RolesFilePaths[0])
	}
}

func TestDataPathLayout(t *testing.T) {
	withServerRoot(t, "/srv/inst")
	t.Setenv("DATA_DIR", "")
	if got := dataPath("app.log"); got != filepath.Join("/srv/inst", "data", "app.log") {
		t.Fatalf("root dataPath = %q", got)
	}
	t.Setenv("DATA_DIR", "/srv/v2v")
	if got := dataPath("history.jsonl"); got != "/srv/v2v/history.jsonl" {
		t.Fatalf("DATA_DIR dataPath = %q", got)
	}
}

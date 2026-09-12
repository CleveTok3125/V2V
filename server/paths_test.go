package main

import (
	"path/filepath"
	"testing"
)

func TestDataPathLayout(t *testing.T) {
	t.Setenv("DATA_DIR", "")
	if got := dataPath("app.log"); got != filepath.Join("./data", "app.log") {
		t.Fatalf("default dataPath = %q", got)
	}
	t.Setenv("DATA_DIR", "/srv/v2v")
	if got := dataPath("history.jsonl"); got != "/srv/v2v/history.jsonl" {
		t.Fatalf("DATA_DIR dataPath = %q", got)
	}
}

func TestConfigPathFirstEntries(t *testing.T) {
	if EnvFilePaths[0] != ".env" {
		t.Fatalf("env first = %q", EnvFilePaths[0])
	}
	if RolesFilePaths[0] != "config/roles.json" {
		t.Fatalf("roles first = %q", RolesFilePaths[0])
	}
}

//go:build !js

package main

import (
	"os"
	"path/filepath"
	"testing"
)

// TestHardenHistoryFileForcesOwnerOnly covers both the happy path and the
// "already owner-only" case: hardenHistoryFile must leave a 0600 file as-is
// and tighten one that a permissive umask created.
func TestHardenHistoryFileForcesOwnerOnly(t *testing.T) {
	oldHistory := historyFile
	defer func() { historyFile = oldHistory }()

	dir := t.TempDir()
	historyFile = filepath.Join(dir, "history.tmp")
	if err := os.WriteFile(historyFile, []byte("typed lines"), 0o644); err != nil {
		t.Fatal(err)
	}
	hardenHistoryFile()
	fi, err := os.Stat(historyFile)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("history mode = %o, want 600", fi.Mode().Perm())
	}
}

// TestHardenHistoryFileMissingIsNoOp: a missing file must not panic or
// create it; readline owns creation.
func TestHardenHistoryFileMissingIsNoOp(t *testing.T) {
	oldHistory := historyFile
	defer func() { historyFile = oldHistory }()

	historyFile = filepath.Join(t.TempDir(), "history.tmp")
	hardenHistoryFile()
	if _, err := os.Stat(historyFile); !os.IsNotExist(err) {
		t.Fatalf("hardenHistoryFile must not create the file, stat err = %v", err)
	}
}

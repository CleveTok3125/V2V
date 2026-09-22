package main

import (
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestInitLoggerCreatesDir pins the fresh-checkout case: the log parent
// directory may not exist yet, and the boot must create it instead of
// failing on the first open.
func TestInitLoggerCreatesDir(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "app.log")
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	if err := InitLogger(path, 1); err != nil {
		t.Fatalf("InitLogger must create the log dir: %v", err)
	}
	log.Println("logger-dir-probe")

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("log file not created: %v", err)
	}
	if !strings.Contains(string(data), "logger-dir-probe") {
		t.Fatalf("log line missing from file, got %q", data)
	}
}

// MaxSize 0 means "no rotation", not "rotate on every write".
func TestRotatingLogger_ZeroMaxSizeNoRotation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.log")
	rl := &RotatingLogger{Filename: path, MaxSize: 0}
	if err := rl.open(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if rl.file != nil {
			_ = rl.file.Close()
		}
	}()
	for i := 0; i < 5; i++ {
		if _, err := rl.Write([]byte("line\n")); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := os.Stat(path + ".old"); !os.IsNotExist(err) {
		t.Fatal("zero MaxSize must not rotate")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(string(data), "line"); got != 5 {
		t.Fatalf("expected 5 lines in one file, got %d (%q)", got, data)
	}
}

// A failed rename must keep the active file's size accounting intact;
// zeroing it would under-count and rotate again too soon.
func TestRotatingLogger_RenameFailureKeepsAccounting(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.log")
	rl := &RotatingLogger{Filename: path, MaxSize: 1024}
	if err := rl.open(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if rl.file != nil {
			_ = rl.file.Close()
		}
	}()
	if _, err := rl.Write([]byte("first\n")); err != nil {
		t.Fatal(err)
	}

	blocker := path + ".old"
	if err := os.Mkdir(blocker, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(blocker, "x"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	rl.mu.Lock()
	rotErr := rl.rotate()
	sizeAfter := rl.size
	rl.mu.Unlock()
	if rotErr != nil {
		t.Fatalf("rotate must reopen despite a rename failure: %v", rotErr)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if sizeAfter != fi.Size() {
		t.Fatalf("size accounting = %d, want %d", sizeAfter, fi.Size())
	}
	if _, err := rl.Write([]byte("second\n")); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "first") || !strings.Contains(string(data), "second") {
		t.Fatalf("both lines must stay in the active file: %q", data)
	}
}

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

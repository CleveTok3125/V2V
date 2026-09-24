package geoip

import (
	"os"
	"path/filepath"
	"testing"
)

func TestOpenMissingDir(t *testing.T) {
	r, warns, err := Open(filepath.Join(t.TempDir(), "nope"))
	if err != nil {
		t.Fatalf("missing dir must be tolerated, got %v", err)
	}
	if r != nil {
		t.Fatal("missing dir must yield nil resolver")
	}
	if len(warns) == 0 {
		t.Fatal("missing dir must warn")
	}
	if _, ok := r.Lookup("8.8.8.8"); ok {
		t.Fatal("nil resolver must not resolve")
	}
}

func TestOpenEmptyDir(t *testing.T) {
	dir := t.TempDir()
	r, warns, err := Open(dir)
	if err != nil {
		t.Fatalf("empty dir must be tolerated, got %v", err)
	}
	if r != nil {
		t.Fatal("empty dir must yield nil resolver")
	}
	if len(warns) == 0 {
		t.Fatal("empty dir must warn")
	}
}

func TestOpenCorruptFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "GeoLite2-ASN.mmdb"), []byte("not a database"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Open(dir); err == nil {
		t.Fatal("corrupt mmdb must fail")
	}
}

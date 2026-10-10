//go:build !windows

package main

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// TestAdoptDirOwnerIgnoresAnAbsentFile: nothing was written, so there is
// nothing to adopt and no reason to fail.
func TestAdoptDirOwnerIgnoresAnAbsentFile(t *testing.T) {
	if err := adoptDirOwner(filepath.Join(t.TempDir(), "webauthn.json")); err != nil {
		t.Fatalf("adoptDirOwner on a missing file: %v", err)
	}
}

// TestAdoptDirOwnerLeavesAMatchingFileAlone: when the writer and the data
// directory already agree, which is every ordinary setup, the file must not
// be touched at all.
func TestAdoptDirOwnerLeavesAMatchingFileAlone(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "webauthn.json")
	if err := os.WriteFile(path, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	if err := adoptDirOwner(path); err != nil {
		t.Fatalf("adoptDirOwner: %v", err)
	}

	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(before, after) {
		t.Fatal("a matching file was replaced instead of left alone")
	}
}

// TestAdoptDirOwnerHandsTheStoreBackToTheDataOwner is the regression: the
// operator ran v2vctl as root, so the store came out root-owned while the
// data directory belongs to the service user, and the server could not read
// its own store until someone chowned it by hand.
func TestAdoptDirOwnerHandsTheStoreBackToTheDataOwner(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("needs root to prove the store is handed to another owner")
	}
	service := 65534
	dir := t.TempDir()
	path := filepath.Join(dir, "webauthn.json")
	if err := os.WriteFile(path, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(dir, service, service); err != nil {
		t.Skipf("cannot chown the directory to %d: %v", service, err)
	}

	if err := adoptDirOwner(path); err != nil {
		t.Fatalf("adoptDirOwner: %v", err)
	}

	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	st := fi.Sys().(*syscall.Stat_t)
	if int(st.Uid) != service || int(st.Gid) != service {
		t.Fatalf("store owner = %d:%d, want the data owner %d:%d", st.Uid, st.Gid, service, service)
	}
}

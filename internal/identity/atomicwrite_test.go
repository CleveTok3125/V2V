package identity

import (
	"os"
	"path/filepath"
	"testing"
)

// TestOwnerForFollowsTheFileThenTheDirectory pins who a replacement should
// belong to. Any writer that runs as a different user than the server must
// not hand the file over on write: the host operator runs v2vctl as root
// while the server reads the same file as its own user.
func TestOwnerForFollowsTheFileThenTheDirectory(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "store.json")

	// No file yet: the directory answers, because that is the owner the
	// operator set up for the service to use.
	uid, gid, ok := ownerFor(path)
	if !ok {
		t.Fatal("a missing file with an existing directory reported no owner")
	}
	if uid != os.Getuid() || gid != os.Getgid() {
		t.Fatalf("owner = %d:%d, want the directory owner %d:%d", uid, gid, os.Getuid(), os.Getgid())
	}

	if err := os.WriteFile(path, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	uid, gid, ok = ownerFor(path)
	if !ok {
		t.Fatal("an existing file reported no owner")
	}
	if uid != os.Getuid() || gid != os.Getgid() {
		t.Fatalf("owner = %d:%d, want the file owner %d:%d", uid, gid, os.Getuid(), os.Getgid())
	}

	// Neither file nor directory: nothing to carry, and that is not an error.
	if _, _, ok := ownerFor(filepath.Join(dir, "missing", "store.json")); ok {
		t.Fatal("neither the file nor its directory exists, yet an owner was reported")
	}
}

// TestAtomicWriteKeepsTheOwner is the regression this exists for: a root
// writer replacing a file owned by the service user used to leave the file
// ownerd by root, and the server then could not read its own store until
// someone ran chown by hand.
func TestAtomicWriteKeepsTheOwner(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("needs root to prove a replacement does not change the owner")
	}
	service := 65534 // nobody, the conventional unprivileged id
	dir := t.TempDir()
	path := filepath.Join(dir, "store.json")
	if err := os.WriteFile(path, []byte(`{"version":3}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(path, service, service); err != nil {
		t.Skipf("cannot chown to %d: %v", service, err)
	}

	if err := atomicWriteFile(path, []byte(`{"version":3,"credentials":{}}`), 0o600); err != nil {
		t.Fatalf("atomicWriteFile: %v", err)
	}

	uid, gid, _ := ownerFor(path)
	if uid != service || gid != service {
		t.Fatalf("owner after write = %d:%d, want %d:%d: the replacement changed hands", uid, gid, service, service)
	}
	// The content must still be the new one, so ownership was not bought
	// by silently skipping the write.
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != `{"version":3,"credentials":{}}` {
		t.Fatalf("content = %s, want the replacement", got)
	}
}

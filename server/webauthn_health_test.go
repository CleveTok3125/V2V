package main

// Store health. The credential store is read on every passkey login and
// written on every enrollment, so a permission or ownership mistake shows up
// in two bad ways: an enrollment that leaks the server's filesystem layout to
// a browser, and logins that silently look like "no such credential".

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// brokenStorePath returns a store path that no process can touch: the parent
// of the file is itself a regular file, so every open and mkdir fails with
// ENOTDIR. That holds for any uid, unlike a chmod, which root ignores.
func brokenStorePath(t *testing.T) string {
	t.Helper()
	blocker := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(blocker, "webauthn.json")
}

// TestVerifyUsableAcceptsAFreshStore: a new instance has no store file yet,
// and that is not a fault — the first enrollment creates it.
func TestVerifyUsableAcceptsAFreshStore(t *testing.T) {
	store := NewWebAuthnStore(filepath.Join(t.TempDir(), "data", "webauthn.json"))
	if err := store.verifyUsable(); err != nil {
		t.Fatalf("a fresh store was rejected: %v", err)
	}
}

// TestVerifyUsableRejectsAnUnreadableStore is the boot-time half of the fix:
// the failure has to surface when the server starts, not at the first
// ceremony hours later.
func TestVerifyUsableRejectsAnUnreadableStore(t *testing.T) {
	store := NewWebAuthnStore(brokenStorePath(t))
	err := store.verifyUsable()
	if err == nil {
		t.Fatal("an unreadable store passed the boot check")
	}
	// The detail belongs in the boot log, so it must be present and useful.
	if !strings.Contains(err.Error(), "webauthn") {
		t.Fatalf("the boot error does not say what failed: %v", err)
	}
}

// TestVerifyUsableRejectsAnUnwritableDirectory: a readable store in a
// directory this process cannot write is a deployment that boots fine and
// then fails every enrollment. Only the write probe catches it.
func TestVerifyUsableRejectsAnUnwritableDirectory(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions, so the probe cannot fail")
	}
	dir := filepath.Join(t.TempDir(), "data")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	store := NewWebAuthnStore(filepath.Join(dir, "webauthn.json"))
	if err := store.verifyUsable(); err != nil {
		t.Fatalf("a writable store was rejected: %v", err)
	}
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	if err := store.verifyUsable(); err == nil {
		t.Fatal("a read-only directory passed the boot check")
	}
}

// TestVerifyUsableRejectsAnUnreadableFileInAWritableDirectory isolates the
// read check. The ENOTDIR case is caught by the directory probe as well, so
// without this one the read check could be deleted and every test would
// still pass while a chmod-000 store booted happily and then failed logins.
func TestVerifyUsableRejectsAnUnreadableFileInAWritableDirectory(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads a 000 file, so the probe cannot fail")
	}
	dir := filepath.Join(t.TempDir(), "data")
	storePath := filepath.Join(dir, "webauthn.json")
	store := NewWebAuthnStore(storePath)
	if err := store.verifyUsable(); err != nil {
		t.Fatalf("a fresh store was rejected: %v", err)
	}
	body := []byte(`{"version":3,"credentials":{}}`)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(storePath, body, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(storePath, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(storePath, 0o600) })

	if err := store.verifyUsable(); err == nil {
		t.Fatal("a store this process cannot read passed the boot check")
	}
}

// TestVerifyUsableRejectsAnOldStore: the store refuses versions it does not
// understand, and the refusal was documented as happening at boot while
// nothing actually read the file there. The check makes the documented
// behaviour true instead of leaving it to the first ceremony.
func TestVerifyUsableRejectsAnOldStore(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	storePath := filepath.Join(dir, "webauthn.json")
	if err := os.WriteFile(storePath, []byte(`{"version":2,"credentials":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}

	err := NewWebAuthnStore(storePath).verifyUsable()
	if err == nil {
		t.Fatal("an unsupported store version passed the boot check")
	}
	if !strings.Contains(err.Error(), "enroll") {
		t.Fatalf("the refusal does not tell the operator what to do: %v", err)
	}
}

// enrollTestServer wires the enrollment handlers against a store at the
// given path, with passkeys enabled and the per-IP cooldowns cleared.
func enrollTestServer(t *testing.T, storePath string) *ChatServer {
	t.Helper()
	setupWA(t)
	enrollBeginCooldowns.Reset()
	enrollFinishCooldowns.Reset()
	s := NewChatServer()
	s.WebAuthn = NewWebAuthnStore(storePath)
	return s
}

// TestEnrollBeginHidesStoreFailures: the response must describe the call the
// caller made, not the server's filesystem. A raw *os.PathError carries the
// absolute store path straight into the browser.
func TestEnrollBeginHidesStoreFailures(t *testing.T) {
	storePath := brokenStorePath(t)
	s := enrollTestServer(t, storePath)

	req := httptest.NewRequest(http.MethodGet, "/webauthn/enroll/begin?ticket=abc", nil)
	rec := httptest.NewRecorder()
	s.handleEnrollBegin(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("code = %d, want 500: a broken store is not the caller's fault", rec.Code)
	}
	body := rec.Body.String()
	for _, leak := range []string{storePath, filepath.Dir(storePath), "not-a-directory", "no such file", "permission denied", "not a directory"} {
		if strings.Contains(body, leak) {
			t.Fatalf("the response leaked %q:\n%s", leak, body)
		}
	}
}

// TestEnrollFinishHidesStoreFailures: same rule on the write path, which is
// the one the reported failure came from.
func TestEnrollFinishHidesStoreFailures(t *testing.T) {
	storePath := brokenStorePath(t)
	s := enrollTestServer(t, storePath)

	body := `{"ticket":"abc","id":"y","client_data_json":"z","attestation_object":"w"}`
	req := httptest.NewRequest(http.MethodPost, "/webauthn/enroll/finish", strings.NewReader(body))
	rec := httptest.NewRecorder()
	s.handleEnrollFinish(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("code = %d, want 500", rec.Code)
	}
	resp := rec.Body.String()
	for _, leak := range []string{storePath, filepath.Dir(storePath), "not-a-directory", "no such file", "permission denied"} {
		if strings.Contains(resp, leak) {
			t.Fatalf("the response leaked %q:\n%s", leak, resp)
		}
	}
}

// TestEnrollBeginStillNamesABadTicket: hiding internal faults must not hide
// the reasons a caller can act on. A ticket nobody issued is 403 and says so.
func TestEnrollBeginStillNamesABadTicket(t *testing.T) {
	s := enrollTestServer(t, filepath.Join(t.TempDir(), "data", "webauthn.json"))

	req := httptest.NewRequest(http.MethodGet, "/webauthn/enroll/begin?ticket=never-issued", nil)
	rec := httptest.NewRecorder()
	s.handleEnrollBegin(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("code = %d, want 403", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), errTicketUnknown.Error()) {
		t.Fatalf("the caller was not told the ticket is bad: %s", rec.Body)
	}
}

// TestCredentialReadFailureIsNotSilent: Credential wraps the store read, and
// a failure there used to be indistinguishable from "no such credential".
// The client still gets the generic auth failure, but the operator has to be
// able to see why.
func TestCredentialReadFailureIsNotSilent(t *testing.T) {
	store := NewWebAuthnStore(brokenStorePath(t))
	cred, ok := store.Credential("member", "some-credential")
	if ok || cred != nil {
		t.Fatalf("an unreadable store returned a credential: %+v", cred)
	}
}

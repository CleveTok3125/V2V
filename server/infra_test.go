package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/CleveTok3125/V2V/internal/env"
)

// Env loader must parse ints/durations/bools and surface errors on
// garbage instead of silently falling back.
func TestEnvLoader_Types(t *testing.T) {
	t.Setenv("V2V_T_INT", "42")
	t.Setenv("V2V_T_DUR", "5s")
	t.Setenv("V2V_T_BOOL", "true")
	t.Setenv("V2V_T_BADINT", "abc")
	if got, err := getEnvAsInt("V2V_T_INT"); err != nil || got != 42 {
		t.Fatalf("int: %d %v", got, err)
	}
	if _, err := getEnvAsInt("V2V_T_BADINT"); err == nil {
		t.Fatal("bad int must error")
	}
	if got, err := getEnvAsDuration("V2V_T_DUR"); err != nil || got != 5*time.Second {
		t.Fatalf("duration: %v %v", got, err)
	}
	if !getEnvAsBoolFallback("V2V_T_BOOL", false) {
		t.Fatal("bool true not parsed")
	}
	if getEnvAsBoolFallback("V2V_T_MISSING_XYZ", true) != true {
		t.Fatal("missing bool must yield fallback")
	}
	if got := getEnvFallback("V2V_T_MISSING_XYZ", "fb"); got != "fb" {
		t.Fatalf("missing string fallback: %q", got)
	}
	// Optional: missing/empty yields "", a value passes through, and a
	// value naming another variable resolves through it (Smart semantics).
	loader := &envLoader{}
	if got := loader.Optional("V2V_T_MISSING_XYZ"); got != "" {
		t.Fatalf("missing optional must be empty, got %q", got)
	}
	t.Setenv("V2V_T_EMPTY", "")
	if got := loader.Optional("V2V_T_EMPTY"); got != "" {
		t.Fatalf("empty optional must be empty, got %q", got)
	}
	t.Setenv("V2V_T_PLAIN", "value")
	if got := loader.Optional("V2V_T_PLAIN"); got != "value" {
		t.Fatalf("optional plain: %q", got)
	}
	t.Setenv("V2V_T_INDIRECT_TARGET", "resolved")
	t.Setenv("V2V_T_INDIRECT", "V2V_T_INDIRECT_TARGET")
	if got := loader.Optional("V2V_T_INDIRECT"); got != "resolved" {
		t.Fatalf("optional indirection: %q", got)
	}
}

// Server identity must persist across restarts: same path yields the
// same keypair, and the public key is a valid 64-hex.
func TestServerIdentity_Persists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "server_identity.json")
	a, err := LoadOrCreateServerIdentity(path)
	if err != nil || a.PublicKey == "" {
		t.Fatalf("create: %+v %v", a, err)
	}
	b, err := LoadOrCreateServerIdentity(path)
	if err != nil || b.PublicKey != a.PublicKey || b.PrivateKey != a.PrivateKey {
		t.Fatal("identity not stable across loads")
	}
	if len(a.PublicKey) != 64 {
		t.Fatalf("pubkey shape: %q", a.PublicKey)
	}
}

// Static file server must negotiate br/gzip pre-compressed variants,
// honor q=0 refusal, and refuse path escape.
func TestWebFilesHandler_EncodingAndEscape(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "app.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "app.txt.gz"), []byte("gzipped"), 0o644); err != nil {
		t.Fatal(err)
	}
	h := webFilesHandler(dir)

	if !acceptsEncoding("gzip, br", "br") || !acceptsEncoding("gzip", "gzip") {
		t.Fatal("explicit encodings must match")
	}
	if acceptsEncoding("gzip;q=0", "gzip") || acceptsEncoding("*", "br") {
		t.Fatal("q=0 refusal and wildcard must not match")
	}

	req := httptest.NewRequest(http.MethodGet, "/app.txt", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("static serve: %d", rec.Code)
	}
	if rec.Header().Get("Content-Encoding") != "gzip" || rec.Body.String() != "gzipped" {
		t.Fatalf("pre-compressed variant not served: enc=%q body=%q",
			rec.Header().Get("Content-Encoding"), rec.Body.String())
	}
	esc := httptest.NewRequest(http.MethodGet, "/../secret", nil)
	escRec := httptest.NewRecorder()
	h.ServeHTTP(escRec, esc)
	if escRec.Code == http.StatusOK && strings.Contains(escRec.Body.String(), "hello") {
		t.Fatal("path escape served file content")
	}
}

// Directories without their own index must 404 instead of listing
// files; directories with an index keep serving it.
func TestWebFilesHandler_NoDirectoryListing(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "blog"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "blog", "cactus.css"), []byte("css"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "docs", "index.html"), []byte("idx"), 0o644); err != nil {
		t.Fatal(err)
	}
	h := webFilesHandler(dir)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/blog/", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("indexless dir = %d, want 404", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "cactus.css") {
		t.Fatal("directory listing leaked filenames")
	}
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, httptest.NewRequest(http.MethodGet, "/docs/", nil))
	if rec2.Code != http.StatusOK || rec2.Body.String() != "idx" {
		t.Fatalf("indexed dir = %d %q, want 200 idx", rec2.Code, rec2.Body.String())
	}
}

// Rotating logger must rotate at the size cap and keep writing.
func TestRotatingLogger_Rotate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v2v.log")
	rl := &RotatingLogger{Filename: path, MaxSize: 64}
	if err := rl.open(); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20; i++ {
		if _, err := rl.Write([]byte(strings.Repeat("x", 32) + "\n")); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := os.Stat(path + ".old"); err != nil {
		t.Fatalf("rotation never produced .old: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("current log missing after rotate: %v", err)
	}
}

// Web asset lookup must prefer the explicit override, then a webterm
// dir next to the executable, then the cwd-relative default, so a
// release binary launched from an arbitrary directory still serves its
// bundle.
func TestResolveWebtermDir(t *testing.T) {
	exeDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(exeDir, "webterm"), 0o755); err != nil {
		t.Fatal(err)
	}

	t.Setenv(env.KeyWebtermDir, "/custom/webterm")
	if got := resolveWebtermDir(exeDir); got != "/custom/webterm" {
		t.Fatalf("override = %q, want /custom/webterm", got)
	}

	t.Setenv(env.KeyWebtermDir, "   ")
	if got := resolveWebtermDir(exeDir); got != filepath.Join(exeDir, "webterm") {
		t.Fatalf("exe-relative = %q, want %q", got, filepath.Join(exeDir, "webterm"))
	}

	t.Setenv(env.KeyWebtermDir, "")
	if got := resolveWebtermDir(t.TempDir()); got != "webterm" {
		t.Fatalf("missing exe dir = %q, want webterm", got)
	}
	if got := resolveWebtermDir(""); got != "webterm" {
		t.Fatalf("empty exeDir = %q, want webterm", got)
	}
}

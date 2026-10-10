package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// withCapturedStdout swaps os.Stdout for a pipe and returns what the body
// prints. Enroll reports the link on stdout, and a link nobody can paste is
// the failure this file is about.
func withCapturedStdout(t *testing.T, body func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = w

	done := make(chan string, 1)
	go func() {
		var buf strings.Builder
		_, _ = io.Copy(&buf, r)
		done <- buf.String()
	}()

	body()
	_ = w.Close()
	os.Stdout = old
	return <-done
}

// writeInstanceEnv drops an .env under the instance root that v2vctl operates
// on, which is where the server reads its own configuration from.
func writeInstanceEnv(t *testing.T, body string) {
	t.Helper()
	dir := filepath.Join("instances", "default")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestEnrollOriginReadsTheInstanceEnv is the bug that produced a bare
// "/web/#enroll=…" link: enroll only looked at the ambient environment, which
// an operator running v2vctl on the host almost never has exported.
func TestEnrollOriginReadsTheInstanceEnv(t *testing.T) {
	t.Setenv("WEBAUTHN_ORIGIN", "")
	withTempDir(t, func() {
		writeInstanceEnv(t, "PORT=10000\nWEBAUTHN_ORIGIN=https://chat.example.com\n")

		if got := enrollOrigin(); got != "https://chat.example.com" {
			t.Fatalf("enrollOrigin() = %q, want the origin from the instance .env", got)
		}
	})
}

// TestEnrollOriginPrefersTheAmbientEnv: an explicit environment variable is a
// deliberate override and has to win over the file.
func TestEnrollOriginPrefersTheAmbientEnv(t *testing.T) {
	t.Setenv("WEBAUTHN_ORIGIN", "https://explicit.example.com")
	withTempDir(t, func() {
		writeInstanceEnv(t, "WEBAUTHN_ORIGIN=https://file.example.com\n")

		if got := enrollOrigin(); got != "https://explicit.example.com" {
			t.Fatalf("enrollOrigin() = %q, want the ambient override", got)
		}
	})
}

// TestEnrollOriginIgnoresCommentedKeys: the template ships every key
// commented out, and treating one as active would mint links for an origin
// nobody configured.
func TestEnrollOriginIgnoresCommentedKeys(t *testing.T) {
	t.Setenv("WEBAUTHN_ORIGIN", "")
	withTempDir(t, func() {
		writeInstanceEnv(t, "# WEBAUTHN_ORIGIN=https://chat.example.com\n")

		if got := enrollOrigin(); got != "" {
			t.Fatalf("enrollOrigin() = %q, want nothing: the only key is commented", got)
		}
	})
}

// TestEnrollOriginToleratesQuotesAndExport: both spellings appear in shell
// configs, and either one silently not matching produces the broken link
// again.
func TestEnrollOriginToleratesQuotesAndExport(t *testing.T) {
	t.Setenv("WEBAUTHN_ORIGIN", "")
	cases := map[string]string{
		`WEBAUTHN_ORIGIN="https://chat.example.com"`:        "\nWEBAUTHN_ORIGIN=\"https://chat.example.com\"\n",
		`export WEBAUTHN_ORIGIN=https://chat.example.com`:   "\nexport WEBAUTHN_ORIGIN=https://chat.example.com\n",
		`spaced WEBAUTHN_ORIGIN = https://chat.example.com`: "\nWEBAUTHN_ORIGIN = https://chat.example.com\n",
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			withTempDir(t, func() {
				writeInstanceEnv(t, body)
				if got := enrollOrigin(); got != "https://chat.example.com" {
					t.Fatalf("enrollOrigin() = %q, want the parsed origin", got)
				}
			})
		})
	}
}

// TestEnrollPrintsAFullLink: the whole point. A relative path cannot be
// pasted into a browser, so the printed link must carry the origin.
func TestEnrollPrintsAFullLink(t *testing.T) {
	t.Setenv("WEBAUTHN_ORIGIN", "")
	withTempDir(t, func() {
		writeInstanceEnv(t, "WEBAUTHN_ORIGIN=https://chat.example.com\n")

		out := withCapturedStdout(t, func() {
			if err := (&EnrollCmd{Role: "member", TTL: time.Minute}).Run(); err != nil {
				t.Fatalf("enroll: %v", err)
			}
		})
		if !strings.Contains(out, "https://chat.example.com/web/#enroll=") {
			t.Fatalf("the link has no origin:\n%s", out)
		}
		if strings.Contains(out, "\n  /web/#enroll=") {
			t.Fatalf("a relative link was printed:\n%s", out)
		}
	})
}

// TestEnrollStripsATrailingSlash: a double slash is a different URL to a
// browser and to the RP origin check.
func TestEnrollStripsATrailingSlash(t *testing.T) {
	t.Setenv("WEBAUTHN_ORIGIN", "")
	withTempDir(t, func() {
		writeInstanceEnv(t, "WEBAUTHN_ORIGIN=https://chat.example.com/\n")

		out := withCapturedStdout(t, func() {
			if err := (&EnrollCmd{Role: "member", TTL: time.Minute}).Run(); err != nil {
				t.Fatalf("enroll: %v", err)
			}
		})
		if strings.Contains(out, "//web/") {
			t.Fatalf("the origin kept its trailing slash:\n%s", out)
		}
		if !strings.Contains(out, "https://chat.example.com/web/#enroll=") {
			t.Fatalf("unexpected link:\n%s", out)
		}
	})
}

// TestEnrollRefusesWithoutAnOrigin: without one there is no link to give, so
// the command must fail instead of printing a path that cannot be used. It
// must also leave no ticket behind for the operator to wonder about.
func TestEnrollRefusesWithoutAnOrigin(t *testing.T) {
	t.Setenv("WEBAUTHN_ORIGIN", "")
	withTempDir(t, func() {
		writeInstanceEnv(t, "PORT=10000\n")

		err := (&EnrollCmd{Role: "member", TTL: time.Minute}).Run()
		if err == nil {
			t.Fatal("enroll succeeded without an origin")
		}
		if !strings.Contains(err.Error(), "WEBAUTHN_ORIGIN") {
			t.Fatalf("the error does not say what is missing: %v", err)
		}
		if _, statErr := os.Stat(defaultWebauthnStore()); statErr == nil {
			t.Fatal("a ticket was written for a link that cannot be used")
		}
	})
}

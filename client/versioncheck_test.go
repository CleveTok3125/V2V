package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/CleveTok3125/V2V/internal/config"
)

func TestDecideVersionCheck(t *testing.T) {
	cases := []struct {
		mode, server, expected string
		want                   versionVerdict
	}{
		{"warn", "v1", "v1", versionMatch},
		{"enforce", "v1", "v1", versionMatch},
		{"warn", "v2", "v1", versionMismatch},
		{"enforce", "v2", "v1", versionMismatch},
		{"warn", "", "v1", versionUnknown},
		{"enforce", "", "v1", versionUnknown},
		{"warn", "fork-2024.1-custom", "fork-2024.1-custom", versionMatch},
		{"warn", "dev-a1b2c3", "v0.9.0", versionMismatch},
	}
	for _, c := range cases {
		if got := decideVersionCheck(c.mode, c.server, c.expected); got != c.want {
			t.Errorf("decide(%s,%q,%q) = %v, want %v", c.mode, c.server, c.expected, got, c.want)
		}
	}
}

func TestFetchServerVersion(t *testing.T) {
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"version":"s1"}`))
	}))
	defer ok.Close()
	if got := fetchServerVersion(ok.URL); got != "s1" {
		t.Fatalf("version = %q, want s1", got)
	}
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer bad.Close()
	if got := fetchServerVersion(bad.URL); got != "" {
		t.Fatalf("404 must yield unknown, got %q", got)
	}
	junk := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("not json"))
	}))
	defer junk.Close()
	if got := fetchServerVersion(junk.URL); got != "" {
		t.Fatalf("bad JSON must yield unknown, got %q", got)
	}
}

func TestHTTPBaseFromWS(t *testing.T) {
	base, err := httpBaseFromWS("wss://chat.example.com/ws")
	if err != nil || base != "https://chat.example.com" {
		t.Fatalf("base = %q, %v", base, err)
	}
	base, err = httpBaseFromWS("ws://127.0.0.1:8080/ws")
	if err != nil || base != "http://127.0.0.1:8080" {
		t.Fatalf("base = %q, %v", base, err)
	}
	if _, err := httpBaseFromWS("https://chat.example.com/"); err == nil {
		t.Fatal("http scheme must be rejected")
	}
}

func TestVersionCheckAccessors(t *testing.T) {
	c := config.DefaultClientConfig()
	if !c.VersionCheckEnabled() {
		t.Fatal("default must enable the check")
	}
	if c.VersionCheckMode() != "warn" {
		t.Fatalf("default mode = %q, want warn", c.VersionCheckMode())
	}
	if c.VersionCheckExpect() != "" {
		t.Fatal("default expect must be empty")
	}
	c.UI.VersionCheck.Mode = "bogus"
	if c.VersionCheckMode() != "warn" {
		t.Fatal("bogus mode must normalize to warn")
	}
	c.UI.VersionCheck.Mode = "enforce"
	if c.VersionCheckMode() != "enforce" {
		t.Fatal("enforce must survive")
	}
	var nilCfg *config.ClientConfig
	if !nilCfg.VersionCheckEnabled() || nilCfg.VersionCheckMode() != "warn" || nilCfg.VersionCheckExpect() != "" {
		t.Fatal("nil config must fall back to defaults")
	}
}

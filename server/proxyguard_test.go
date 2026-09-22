package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/CleveTok3125/V2V/internal/trustedproxy"
)

// setupProxyChain builds the global test chain from a tmpdir trust
// layout, restoring the previous chain after the test.
func setupProxyChain(t *testing.T, chain string, files map[string]string) {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	names, err := trustedproxy.ParseChain(chain)
	if err != nil {
		t.Fatal(err)
	}
	c, err := initProxyChain(dir, names)
	if err != nil {
		t.Fatal(err)
	}
	old := ProxyChain
	ProxyChain = c
	t.Cleanup(func() { ProxyChain = old })
}

func proxyTestReq(remoteAddr, cfIP string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "http://example.com/ws", nil)
	r.RemoteAddr = remoteAddr
	if cfIP != "" {
		r.Header.Set("CF-Connecting-IP", cfIP)
	}
	return r
}

func TestResolveClientIPStrictRejectsSpoof(t *testing.T) {
	testCfg(t)
	setupProxyChain(t, "cloudflare", map[string]string{
		"cloudflare.txt": "173.245.48.0/20\n",
	})
	out := resolveClientIP(proxyTestReq("198.51.100.9:1234", "10.9.9.9"))
	if !out.Reject {
		t.Fatalf("spoof must reject, got %+v", out)
	}
	if got := getClientIP(proxyTestReq("198.51.100.9:1234", "10.9.9.9")); got != "" {
		t.Fatalf("rejected request must resolve empty, got %q", got)
	}
}

func TestResolveClientIPTrustedHeader(t *testing.T) {
	testCfg(t)
	setupProxyChain(t, "cloudflare,direct", map[string]string{
		"cloudflare.txt": "173.245.48.0/20\n",
		"direct.txt":     "203.0.113.7/32\n",
	})
	if got := getClientIP(proxyTestReq("173.245.48.5:443", "198.51.100.9")); got != "198.51.100.9" {
		t.Fatalf("trusted header = %q", got)
	}
	if got := getClientIP(proxyTestReq("203.0.113.7:555", "10.9.9.9")); got != "203.0.113.7" {
		t.Fatalf("direct member must resolve RemoteAddr, got %q", got)
	}
}

func TestInitProxyChainMissingFileFails(t *testing.T) {
	dir := t.TempDir()
	names, err := trustedproxy.ParseChain("cloudflare")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := initProxyChain(dir, names); err == nil {
		t.Fatal("missing cloudflare.txt must fail closed")
	}
	if _, err := trustedproxy.ParseChain(""); err == nil {
		t.Fatal("empty PROXY_PROVIDER must fail")
	}
}

func TestServeWSRejectsUntrusted(t *testing.T) {
	testCfg(t)
	setupProxyChain(t, "cloudflare", map[string]string{
		"cloudflare.txt": "173.245.48.0/20\n",
	})
	s := NewChatServer()
	rec := httptest.NewRecorder()
	s.ServeWS(rec, proxyTestReq("198.51.100.9:1234", "10.9.9.9"))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("ServeWS untrusted = %d, want 403", rec.Code)
	}
}

// Non-WebSocket endpoints must share the ServeWS proxy gate, otherwise a
// rejected request resolves an empty client IP and pollutes one bucket.
func TestRejectUntrustedProxy(t *testing.T) {
	testCfg(t)
	setupProxyChain(t, "cloudflare", map[string]string{
		"cloudflare.txt": "173.245.48.0/20\n",
	})
	rec := httptest.NewRecorder()
	if !rejectUntrustedProxy(rec, proxyTestReq("198.51.100.9:1234", "10.9.9.9")) {
		t.Fatal("untrusted request must be rejected")
	}
	if rec.Code != http.StatusForbidden {
		t.Fatalf("untrusted code = %d, want 403", rec.Code)
	}
	rec2 := httptest.NewRecorder()
	if rejectUntrustedProxy(rec2, proxyTestReq("173.245.48.5:443", "198.51.100.9")) {
		t.Fatal("trusted request must pass")
	}
}

func TestIsSecuredConnectGatesForwardedProto(t *testing.T) {
	testCfg(t)
	old := Cfg.Static.RequireTLS
	Cfg.Static.RequireTLS = true
	t.Cleanup(func() { Cfg.Static.RequireTLS = old })

	r := proxyTestReq("198.51.100.9:1234", "")
	r.Header.Set("X-Forwarded-Proto", "https")
	rec := httptest.NewRecorder()
	untrusted := trustedproxy.Outcome{ClientIP: "198.51.100.9", RemoteIP: "198.51.100.9"}
	if IsSecuredConnect(rec, r, untrusted) {
		t.Fatal("X-Forwarded-Proto from untrusted must not secure")
	}
	if rec.Code != http.StatusUpgradeRequired {
		t.Fatalf("unsecured code = %d, want 426", rec.Code)
	}
	rec2 := httptest.NewRecorder()
	trusted := trustedproxy.Outcome{ClientIP: "198.51.100.9", RemoteIP: "198.51.100.9", Trusted: true, Provider: "cloudflare"}
	if !IsSecuredConnect(rec2, r, trusted) {
		t.Fatal("X-Forwarded-Proto from trusted proxy must secure")
	}
}

func TestTrustedProxyDefaultDir(t *testing.T) {
	if DefaultTrustedProxyDir != "config/trustedproxy" {
		t.Fatalf("default trust dir = %q", DefaultTrustedProxyDir)
	}
}

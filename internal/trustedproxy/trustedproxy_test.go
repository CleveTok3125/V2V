package trustedproxy

import (
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func mustParseCIDRs(t *testing.T, cidrs ...string) []*net.IPNet {
	t.Helper()
	var out []*net.IPNet
	for _, c := range cidrs {
		_, n, err := net.ParseCIDR(c)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, n)
	}
	return out
}

func mkRequest(remoteAddr, cfIP string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "http://example.com/ws", nil)
	r.RemoteAddr = remoteAddr
	if cfIP != "" {
		r.Header.Set("CF-Connecting-IP", cfIP)
	}
	return r
}

func TestParseChain(t *testing.T) {
	got, err := ParseChain("cloudflare,direct")
	if err != nil || len(got) != 2 || got[0] != "cloudflare" || got[1] != "direct" {
		t.Fatalf("ParseChain = %v, %v", got, err)
	}
	got, err = ParseChain(" CloudFlare , cloudflare ,,NONE ")
	if err != nil || len(got) != 2 || got[0] != "cloudflare" || got[1] != "none" {
		t.Fatalf("ParseChain case/dedupe = %v, %v", got, err)
	}
	for _, raw := range []string{"", "  ", "squid", "cloudflare,squid"} {
		if _, err := ParseChain(raw); err == nil {
			t.Errorf("ParseChain(%q) must fail", raw)
		}
	}
	// The shipped sentinel counts as unconfigured and must demand an
	// explicit chain, not report an unknown provider.
	for _, raw := range []string{"false", "FALSE", " false "} {
		_, err := ParseChain(raw)
		if err == nil || !strings.Contains(err.Error(), "required") {
			t.Errorf("ParseChain(%q) must demand explicit config, got %v", raw, err)
		}
	}
}

func TestChainResolveMatrix(t *testing.T) {
	cfNets := mustParseCIDRs(t, "173.245.48.0/20", "2606:4700::/32")
	officeNets := mustParseCIDRs(t, "203.0.113.7/32")
	sets := map[string][]*net.IPNet{"cloudflare": cfNets, "direct": officeNets}
	newChain := func(t *testing.T, names ...string) *Chain {
		t.Helper()
		c, err := NewChain(names, sets)
		if err != nil {
			t.Fatal(err)
		}
		return c
	}

	// Via trusted CF edge: header wins.
	out := newChain(t, "cloudflare", "direct").Resolve(mkRequest("173.245.48.5:443", "198.51.100.9"))
	if out.Reject || out.ClientIP != "198.51.100.9" || out.Provider != "cloudflare" || !out.Trusted {
		t.Errorf("cf trusted = %+v", out)
	}
	// Spoof attempt: header from untrusted address is ignored, strict rejects.
	out = newChain(t, "cloudflare").Resolve(mkRequest("198.51.100.9:1234", "10.9.9.9"))
	if !out.Reject || out.Provider != "" {
		t.Errorf("spoof must reject, got %+v", out)
	}
	// Same spoof with none fallback: direct with warning (not trusted).
	out = newChain(t, "cloudflare", "none").Resolve(mkRequest("198.51.100.9:1234", "10.9.9.9"))
	if out.Reject || out.ClientIP != "198.51.100.9" || out.Trusted || out.Provider != "none" {
		t.Errorf("none fallback = %+v", out)
	}
	// Listed direct IP: direct, never reads headers.
	out = newChain(t, "cloudflare", "direct").Resolve(mkRequest("203.0.113.7:555", "10.9.9.9"))
	if out.Reject || out.ClientIP != "203.0.113.7" || out.Trusted || out.Provider != "direct" {
		t.Errorf("direct member = %+v", out)
	}
	// Trusted edge, missing header: reject (nothing to resolve).
	out = newChain(t, "cloudflare").Resolve(mkRequest("173.245.48.5:443", ""))
	if !out.Reject || out.Provider != "cloudflare" {
		t.Errorf("missing header must reject, got %+v", out)
	}
	// Trusted edge, garbage header: reject.
	out = newChain(t, "cloudflare").Resolve(mkRequest("173.245.48.5:443", "not-an-ip"))
	if !out.Reject {
		t.Errorf("garbage header must reject, got %+v", out)
	}
	// none alone: everything is direct.
	out = newChain(t, "none").Resolve(mkRequest("203.0.113.99:1", "10.9.9.9"))
	if out.Reject || out.ClientIP != "203.0.113.99" || out.Provider != "none" {
		t.Errorf("none alone = %+v", out)
	}
	// IPv6 edge.
	out = newChain(t, "cloudflare").Resolve(mkRequest("[2606:4700::1]:443", "2001:db8::9"))
	if out.Reject || out.ClientIP != "2001:db8::9" || !out.Trusted {
		t.Errorf("ipv6 edge = %+v", out)
	}
}

func TestNewChainEmptyTrustFails(t *testing.T) {
	if _, err := NewChain([]string{"cloudflare"}, map[string][]*net.IPNet{}); err == nil {
		t.Fatal("cloudflare with empty trust set must fail")
	}
	if _, err := NewChain([]string{"none"}, map[string][]*net.IPNet{}); err != nil {
		t.Fatalf("none needs no trust set: %v", err)
	}
}

func TestLoadTrustDir(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("cloudflare.txt", "# CF edge\n173.245.48.0/20\n2606:4700::/32\n\n203.0.113.7\n")
	sets, warn, err := LoadTrustDir(dir, []string{"cloudflare", "direct"}, map[string]bool{"cloudflare": true})
	if err != nil {
		t.Fatal(err)
	}
	if warn != "" {
		t.Fatalf("unexpected warning: %q", warn)
	}
	if len(sets["cloudflare"].Nets) != 3 {
		t.Fatalf("cloudflare nets = %d, want 3", len(sets["cloudflare"].Nets))
	}
	if !sets["direct"].Missing {
		t.Fatal("missing optional direct.txt must report Missing, not fail")
	}

	write("direct.txt", "not-an-ip\n")
	if _, _, err := LoadTrustDir(dir, []string{"direct"}, map[string]bool{}); err == nil ||
		!strings.Contains(err.Error(), "direct.txt:1") {
		t.Fatalf("bad line must fail with file:line, got %v", err)
	}

	if _, _, err := LoadTrustDir(dir, []string{"cloudflare", "direct"}, map[string]bool{"cloudflare": true}); err == nil {
		t.Fatal("direct.txt still broken from above; sanity")
	}
	os.Remove(filepath.Join(dir, "cloudflare.txt"))
	if _, _, err := LoadTrustDir(dir, []string{"cloudflare"}, map[string]bool{"cloudflare": true}); err == nil {
		t.Fatal("missing required cloudflare.txt must fail")
	}

	os.Remove(filepath.Join(dir, "direct.txt"))
	write("cloudflare.txt", "173.245.48.0/20\n")
	for i := 0; i < 12; i++ {
		write("stray"+string(rune('a'+i))+".txt", "10.0.0.1\n")
	}
	_, warn, err = LoadTrustDir(dir, []string{"cloudflare"}, map[string]bool{"cloudflare": true})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(warn, "extra trust files ignored: 12 (") {
		t.Fatalf("stray files must fold into one line, got %q", warn)
	}
	if strings.Count(warn, "\n") != 0 {
		t.Fatalf("warning must be a single line: %q", warn)
	}

	if _, _, err := LoadTrustDir(filepath.Join(dir, "nope"), []string{"cloudflare"}, map[string]bool{"cloudflare": true}); err == nil {
		t.Fatal("missing dir must fail")
	}
}

func TestClip(t *testing.T) {
	if got := Clip("abcdef", 10); got != "abcdef" {
		t.Errorf("short Clip = %q", got)
	}
	if got := Clip("abcdef", 4); got != "abcd…" {
		t.Errorf("long Clip = %q", got)
	}
	if got := Clip("日本語テスト", 4); got != "日本語テ…" {
		t.Errorf("multibyte Clip = %q", got)
	}
}

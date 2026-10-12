//go:build !js

package clientcore

import (
	"errors"
	"strings"
	"testing"
)

func TestParseProxyURL(t *testing.T) {
	cfg, err := ParseProxyURL("socks5://127.0.0.1:1080")
	if err != nil || cfg.Scheme != "socks5" || cfg.Host != "127.0.0.1" || cfg.Port != 1080 {
		t.Errorf("socks5 = %+v, %v", cfg, err)
	}
	cfg, err = ParseProxyURL("http://user:secret@proxy.local")
	if err != nil || cfg.Port != 8080 || cfg.User != "user" || string(cfg.Pass) != "secret" {
		t.Errorf("http default port/auth = %+v, %v", cfg, err)
	}
	cfg, err = ParseProxyURL("socks5h://proxy.local:1080")
	if err != nil || cfg.Scheme != "socks5" {
		t.Errorf("socks5h alias = %+v, %v", cfg, err)
	}
	for _, raw := range []string{
		"ftp://proxy.local:21",
		"http://:8080",
		"http://:secret@proxy.local:8080",
		"http://proxy.local:99999",
		"http://proxy.local:abc",
		"://bad",
		"",
	} {
		if _, err := ParseProxyURL(raw); err == nil {
			t.Errorf("ParseProxyURL(%q) must fail", raw)
		}
	}
}

func TestParseProxyURLAuthMatrix(t *testing.T) {
	cases := []struct {
		raw      string
		user     string
		pass     string
		hasPass  bool
		failFast bool
	}{
		{"http://proxy.local:8080", "", "", false, false},
		{"http://user@proxy.local:8080", "user", "", false, false},
		{"http://user:pass@proxy.local:8080", "user", "pass", true, false},
		{"http://user:@proxy.local:8080", "user", "", true, false},
		{"http://:pass@proxy.local:8080", "", "", false, true},
	}
	for _, c := range cases {
		cfg, err := ParseProxyURL(c.raw)
		if c.failFast {
			if err == nil {
				t.Errorf("ParseProxyURL(%q) must fail fast", c.raw)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseProxyURL(%q): %v", c.raw, err)
			continue
		}
		if cfg.User != c.user {
			t.Errorf("ParseProxyURL(%q) user = %q, want %q", c.raw, cfg.User, c.user)
		}
		if got := string(cfg.Pass); got != c.pass {
			t.Errorf("ParseProxyURL(%q) pass = %q, want %q", c.raw, got, c.pass)
		}
		if (cfg.Pass != nil) != c.hasPass {
			t.Errorf("ParseProxyURL(%q) pass presence = %v, want %v",
				c.raw, cfg.Pass != nil, c.hasPass)
		}
	}
}

func TestProxyLogStringMasksPassword(t *testing.T) {
	s := (&ProxyConfig{Scheme: "http", Host: "h", Port: 8080, User: "u", Pass: []byte("secret")}).LogString()
	if strings.Contains(s, "secret") || !strings.Contains(s, "u:***@h:8080") {
		t.Errorf("password must be masked: %q", s)
	}
	s = (&ProxyConfig{Scheme: "socks5", Host: "h", Port: 1080}).LogString()
	if s != "socks5://h:1080" {
		t.Errorf("no-auth form: %q", s)
	}
}

func TestProxyWipe(t *testing.T) {
	cfg, err := ParseProxyURL("http://u:p@h:8080")
	if err != nil {
		t.Fatal(err)
	}
	cfg.Wipe()
	if cfg.Pass != nil {
		t.Error("wipe must drop the password bytes")
	}
	if cfg.User != "u" || cfg.Host != "h" {
		t.Errorf("wipe must keep non-sensitive fields: %+v", cfg)
	}
	var nilCfg *ProxyConfig
	nilCfg.Wipe() // must not panic
}

// A caller words the refusal itself, so the reason and the offending value
// have to survive the trip out of the parser.
func TestParseProxyURLReportsTheReason(t *testing.T) {
	cases := []struct {
		raw    string
		reason ProxyErrorReason
		value  string
	}{
		{"://bad", ProxyURLInvalid, ""},
		{"", ProxySchemeUnsupported, ""},
		{"ftp://proxy.local:21", ProxySchemeUnsupported, "ftp"},
		{"http://:8080", ProxyHostMissing, ""},
		{"http://proxy.local:99999", ProxyPortInvalid, "99999"},
		// url.Parse rejects a non-numeric port before the range check.
		{"http://proxy.local:abc", ProxyURLInvalid, ""},
		{"http://:secret@proxy.local:8080", ProxyUsernameMissing, ""},
	}
	for _, c := range cases {
		_, err := ParseProxyURL(c.raw)
		var refused *ProxyURLError
		if !errors.As(err, &refused) {
			t.Errorf("ParseProxyURL(%q) err = %v, want a *ProxyURLError", c.raw, err)
			continue
		}
		if refused.Reason != c.reason || refused.Value != c.value {
			t.Errorf("ParseProxyURL(%q) = reason %d value %q, want %d %q",
				c.raw, refused.Reason, refused.Value, c.reason, c.value)
		}
	}
}

// The fallback wording is English: the library carries no user-facing text.
func TestProxyURLErrorFallbackIsNotLocalized(t *testing.T) {
	for _, raw := range []string{"://bad", "ftp://h:1", "http://:1", "http://h:99999", "http://:p@h:1"} {
		_, err := ParseProxyURL(raw)
		if err == nil {
			t.Fatalf("ParseProxyURL(%q) must fail", raw)
		}
		for _, r := range err.Error() {
			if r > 127 {
				t.Fatalf("ParseProxyURL(%q) err = %q, want ASCII only", raw, err.Error())
			}
		}
	}
}

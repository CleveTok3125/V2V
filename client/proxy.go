package main

// Outbound proxy support (desktop): --proxy / V2V_PROXY for scripted
// use, --ask-proxy for an interactive wizard. HTTP(S) proxies ride
// the gorilla Dialer; SOCKS5 handshakes via x/net/proxy (RFC 1928/1929)
// so gorilla itself owns TLS for wss targets. Hostnames always go to
// the proxy unresolved: no local DNS lookup leaks the target. The
// proxy password is the proxy's secret, not the project's: no meter,
// no weak gate, and it wipes from RAM right after dial. WASM never
// runs this file; the browser owns proxying there.

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"

	"github.com/CleveTok3125/V2V/internal/identity"
)

// proxyConfig is a parsed, validated proxy endpoint. Pass lives as
// bytes so it can wipe after dial; it stays in RAM only.
type proxyConfig struct {
	Scheme string // http, https, socks5
	Host   string
	Port   int
	User   string
	Pass   []byte
}

// wipe clears the password bytes. Non-sensitive fields stay for
// logging and redials within the session.
func (p *proxyConfig) wipe() {
	if p == nil {
		return
	}
	identity.ZeroBytes(p.Pass)
	p.Pass = nil
}

// defaultProxyPort suggests a port per scheme. Enter accepts it.
func defaultProxyPort(scheme string) int {
	if scheme == "socks5" {
		return 1080
	}
	return 8080
}

// parseProxyURL validates a proxy URL string. Empty host, unknown
// scheme, or a bad port fails fast instead of dialing blind.
func parseProxyURL(raw string) (*proxyConfig, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u == nil {
		return nil, errors.New("proxy URL không hợp lệ")
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme == "socks5h" {
		scheme = "socks5" // same thing: the proxy always resolves
	}
	switch scheme {
	case "http", "https", "socks5":
	default:
		return nil, fmt.Errorf("proxy scheme không hỗ trợ: %q (dùng http/https/socks5)", u.Scheme)
	}
	if u.Hostname() == "" {
		return nil, errors.New("proxy thiếu host")
	}
	port := defaultProxyPort(scheme)
	if u.Port() != "" {
		p, err := strconv.Atoi(u.Port())
		if err != nil || p < 1 || p > 65535 {
			return nil, fmt.Errorf("proxy port không hợp lệ: %q", u.Port())
		}
		port = p
	}
	cfg := &proxyConfig{Scheme: scheme, Host: u.Hostname(), Port: port}
	if u.User != nil {
		cfg.User = u.User.Username()
		if pw, ok := u.User.Password(); ok {
			if cfg.User == "" {
				// A password without a username would ride no
				// auth at all: fail here instead of dropping
				// the secret silently and failing at the proxy.
				return nil, errors.New("proxy có password nhưng thiếu username")
			}
			cfg.Pass = []byte(pw)
		}
	}
	return cfg, nil
}

// dialURL rebuilds the endpoint for gorilla's ProxyURL (http/https).
func (p *proxyConfig) dialURL() *url.URL {
	u := &url.URL{Scheme: p.Scheme, Host: net.JoinHostPort(p.Host, strconv.Itoa(p.Port))}
	if p.User != "" {
		u.User = url.UserPassword(p.User, string(p.Pass))
	}
	return u
}

// logString renders the endpoint with the password masked. The user
// is not sensitive; password bytes never appear in any log.
func (p *proxyConfig) logString() string {
	auth := ""
	if p.User != "" {
		auth = p.User + ":***@"
	}
	return fmt.Sprintf("%s://%s%s", p.Scheme, auth, net.JoinHostPort(p.Host, strconv.Itoa(p.Port)))
}

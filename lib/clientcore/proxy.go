package clientcore

// Outbound proxy support: --proxy / V2V_PROXY for scripted use, --ask-proxy
// for an interactive wizard in the built-in client. HTTP(S) proxies ride
// the gorilla Dialer; SOCKS5 handshakes via x/net/proxy (RFC 1928/1929)
// so gorilla itself owns TLS for wss targets. Hostnames always go to
// the proxy unresolved: no local DNS lookup leaks the target. The
// proxy password is the proxy's secret, not the project's: no meter,
// no weak gate, and it wipes from RAM right after dial. WASM never
// runs this file; the browser owns proxying there.

import (
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"

	"github.com/CleveTok3125/V2V/lib/identity"
)

// ProxyConfig is a parsed, validated proxy endpoint. Pass lives as
// bytes so it can wipe after dial; it stays in RAM only.
type ProxyConfig struct {
	Scheme string // http, https, socks5
	Host   string
	Port   int
	User   string
	Pass   []byte
}

// wipe clears the password bytes. Non-sensitive fields stay for
// logging and redials within the session.
func (p *ProxyConfig) Wipe() {
	if p == nil {
		return
	}
	identity.ZeroBytes(p.Pass)
	p.Pass = nil
}

// DefaultProxyPort suggests a port per scheme. Enter accepts it.
func DefaultProxyPort(scheme string) int {
	if scheme == "socks5" {
		return 1080
	}
	return 8080
}

// ProxyErrorReason names why a proxy URL was refused. The core reports the
// reason and the offending value; the caller words it.
type ProxyErrorReason int

const (
	// ProxyURLInvalid: the string does not parse as a URL.
	ProxyURLInvalid ProxyErrorReason = iota
	// ProxySchemeUnsupported: the scheme is not http, https or socks5.
	ProxySchemeUnsupported
	// ProxyHostMissing: the URL names no host.
	ProxyHostMissing
	// ProxyPortInvalid: the port is out of range or not a number.
	ProxyPortInvalid
	// ProxyUsernameMissing: a password arrived with no username, which would
	// ride no auth at all.
	ProxyUsernameMissing
)

// ProxyURLError is a refused proxy URL. Value carries the offending part for
// the reasons that name one. Error is a fallback for a caller that does not
// translate the reason itself.
type ProxyURLError struct {
	Reason ProxyErrorReason
	Value  string
}

func (e *ProxyURLError) Error() string {
	switch e.Reason {
	case ProxySchemeUnsupported:
		return "unsupported proxy scheme: " + e.Value
	case ProxyHostMissing:
		return "proxy host missing"
	case ProxyPortInvalid:
		return "invalid proxy port: " + e.Value
	case ProxyUsernameMissing:
		return "proxy password without a username"
	}
	return "invalid proxy URL"
}

// ParseProxyURL validates a proxy URL string. Empty host, unknown
// scheme, or a bad port fails fast instead of dialing blind.
func ParseProxyURL(raw string) (*ProxyConfig, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u == nil {
		return nil, &ProxyURLError{Reason: ProxyURLInvalid}
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme == "socks5h" {
		scheme = "socks5" // same thing: the proxy always resolves
	}
	switch scheme {
	case "http", "https", "socks5":
	default:
		return nil, &ProxyURLError{Reason: ProxySchemeUnsupported, Value: u.Scheme}
	}
	if u.Hostname() == "" {
		return nil, &ProxyURLError{Reason: ProxyHostMissing}
	}
	port := DefaultProxyPort(scheme)
	if u.Port() != "" {
		p, err := strconv.Atoi(u.Port())
		if err != nil || p < 1 || p > 65535 {
			return nil, &ProxyURLError{Reason: ProxyPortInvalid, Value: u.Port()}
		}
		port = p
	}
	cfg := &ProxyConfig{Scheme: scheme, Host: u.Hostname(), Port: port}
	if u.User != nil {
		cfg.User = u.User.Username()
		if pw, ok := u.User.Password(); ok {
			if cfg.User == "" {
				// A password without a username would ride no
				// auth at all: fail here instead of dropping
				// the secret silently and failing at the proxy.
				return nil, &ProxyURLError{Reason: ProxyUsernameMissing}
			}
			cfg.Pass = []byte(pw)
		}
	}
	return cfg, nil
}

// dialURL rebuilds the endpoint for gorilla's ProxyURL (http/https).
func (p *ProxyConfig) DialURL() *url.URL {
	u := &url.URL{Scheme: p.Scheme, Host: net.JoinHostPort(p.Host, strconv.Itoa(p.Port))}
	if p.User != "" {
		u.User = url.UserPassword(p.User, string(p.Pass))
	}
	return u
}

// logString renders the endpoint with the password masked. The user
// is not sensitive; password bytes never appear in any log.
func (p *ProxyConfig) LogString() string {
	auth := ""
	if p.User != "" {
		auth = p.User + ":***@"
	}
	return fmt.Sprintf("%s://%s%s", p.Scheme, auth, net.JoinHostPort(p.Host, strconv.Itoa(p.Port)))
}

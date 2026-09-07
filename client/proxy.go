package main

// Outbound proxy support (desktop): --proxy / V2V_PROXY for scripted
// use, --ask-proxy for an interactive wizard. HTTP(S) proxies ride
// the gorilla Dialer; SOCKS5 handshakes by hand on a raw socket so no
// new dependency is needed. Hostnames always go to the proxy
// unresolved: no local DNS lookup leaks the target. The proxy
// password is the proxy's secret, not the project's: no meter, no
// weak gate, and it wipes from RAM right after dial. WASM never runs
// this file; the browser owns proxying there.

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"

	"github.com/CleveTok3125/V2V/identity"
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


// socks5Handshake runs the client side of SOCKS5 (RFC 1928) over an
// open TCP connection to the proxy: greeting, optional
// username/password auth, then CONNECT host:port. The target travels
// as a domain name whenever it is not an IP literal, so the proxy
// resolves it and no local DNS leaks. Only the reply code is
// checked; the bound address is consumed and discarded. Auth buffers
// wipe before return.
func socks5Handshake(conn net.Conn, host string, port int, user, pass []byte) error {
	var methods []byte
	if len(user) != 0 {
		methods = []byte{0x02} // username/password
	} else {
		methods = []byte{0x00} // no auth
	}
	if _, err := conn.Write(append([]byte{0x05, byte(len(methods))}, methods...)); err != nil {
		return fmt.Errorf("socks5 greeting: %w", err)
	}
	resp := make([]byte, 2)
	if err := readFull(conn, resp); err != nil {
		return fmt.Errorf("socks5 method: %w", err)
	}
	if resp[0] != 0x05 {
		return fmt.Errorf("socks5 phiên bản lạ: %d", resp[0])
	}
	switch resp[1] {
	case 0x00:
	case 0x02:
		if len(user) == 0 {
			return errors.New("socks5 đòi auth nhưng không có username")
		}
		if err := socks5Auth(conn, user, pass); err != nil {
			return err
		}
	case 0xFF:
		return errors.New("socks5 từ chối mọi phương thức auth")
	default:
		return fmt.Errorf("socks5 phương thức lạ: %d", resp[1])
	}
	var addr []byte
	if ip := net.ParseIP(host); ip != nil {
		if v4 := ip.To4(); v4 != nil {
			addr = append([]byte{0x01}, v4...)
		} else {
			addr = append([]byte{0x04}, ip.To16()...)
		}
	} else {
		if len(host) == 0 || len(host) > 255 {
			return errors.New("socks5 hostname không hợp lệ")
		}
		addr = append([]byte{0x03, byte(len(host))}, host...)
	}
	req := append([]byte{0x05, 0x01, 0x00}, addr...)
	req = append(req, byte(port>>8), byte(port))
	if _, err := conn.Write(req); err != nil {
		return fmt.Errorf("socks5 connect: %w", err)
	}
	head := make([]byte, 4)
	if err := readFull(conn, head); err != nil {
		return fmt.Errorf("socks5 reply: %w", err)
	}
	if head[0] != 0x05 {
		return fmt.Errorf("socks5 phiên bản lạ: %d", head[0])
	}
	if head[1] != 0x00 {
		return fmt.Errorf("socks5 connect thất bại (mã %d)", head[1])
	}
	var skip int
	switch head[3] {
	case 0x01:
		skip = 4
	case 0x04:
		skip = 16
	case 0x03:
		ln := make([]byte, 1)
		if err := readFull(conn, ln); err != nil {
			return fmt.Errorf("socks5 reply: %w", err)
		}
		skip = int(ln[0])
	default:
		return fmt.Errorf("socks5 địa chỉ lạ: %d", head[3])
	}
	if err := readFull(conn, make([]byte, skip+2)); err != nil {
		return fmt.Errorf("socks5 reply: %w", err)
	}
	return nil
}

// socks5Auth runs RFC 1929 username/password auth. Empty password is
// allowed: some proxies only check the username. The request buffer
// wipes before return; caller buffers stay the caller's to wipe.
func socks5Auth(conn net.Conn, user, pass []byte) error {
	if len(user) == 0 || len(user) > 255 || len(pass) > 255 {
		return errors.New("socks5 username/password quá dài")
	}
	req := append([]byte{0x01, byte(len(user))}, user...)
	req = append(req, byte(len(pass)))
	req = append(req, pass...)
	defer identity.ZeroBytes(req)
	if _, err := conn.Write(req); err != nil {
		return fmt.Errorf("socks5 auth: %w", err)
	}
	resp := make([]byte, 2)
	if err := readFull(conn, resp); err != nil {
		return fmt.Errorf("socks5 auth: %w", err)
	}
	if resp[1] != 0x00 {
		return errors.New("socks5 sai username/password")
	}
	return nil
}

// readFull is io.ReadFull with a shorter name for handshake code.
func readFull(conn net.Conn, buf []byte) error {
	for len(buf) > 0 {
		n, err := conn.Read(buf)
		buf = buf[n:]
		if err != nil {
			return err
		}
	}
	return nil
}

//go:build !js

package main

// Desktop-only: the wizard, the resolver and the SOCKS5 dialer live in
// client/proxy_other.go; the parsing they build on lives in lib/clientcore.

import (
	"context"
	"net"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/CleveTok3125/V2V/lib/clientcore"
	"github.com/CleveTok3125/V2V/lib/identity"
)

func TestResolveProxyPrecedence(t *testing.T) {
	oldProxy, oldAsk := CLI.Proxy, CLI.AskProxy
	t.Cleanup(func() { CLI.Proxy, CLI.AskProxy = oldProxy, oldAsk })
	t.Setenv("V2V_PROXY", "http://from-env:8080")

	CLI.Proxy, CLI.AskProxy = "socks5://from-flag:1080", false
	cfg, err := resolveProxy(strings.NewReader(""))
	if err != nil || cfg.Host != "from-flag" {
		t.Errorf("flag must win: %+v, %v", cfg, err)
	}
	CLI.Proxy = ""
	cfg, err = resolveProxy(strings.NewReader(""))
	if err != nil || cfg.Host != "from-env" {
		t.Errorf("env fallback: %+v, %v", cfg, err)
	}
	t.Setenv("V2V_PROXY", "")
	cfg, err = resolveProxy(strings.NewReader(""))
	if err != nil || cfg != nil {
		t.Errorf("nothing set must resolve direct: %+v, %v", cfg, err)
	}
}

func TestPromptProxyConfig(t *testing.T) {
	full, err := promptProxyConfig(strings.NewReader("2\nproxy.local\n\nuser\npass\n"))
	if err != nil {
		t.Fatal(err)
	}
	if full.Scheme != "socks5" || full.Host != "proxy.local" || full.Port != 1080 || full.User != "user" || string(full.Pass) != "pass" {
		t.Errorf("full wizard = %+v", full)
	}
	identity.ZeroBytes(full.Pass)
	minimal, err := promptProxyConfig(strings.NewReader("\nproxy.local\n3128\n\n"))
	if err != nil {
		t.Fatal(err)
	}
	if minimal.Scheme != "http" || minimal.Port != 3128 || minimal.User != "" {
		t.Errorf("minimal wizard = %+v", minimal)
	}
	if _, err := promptProxyConfig(strings.NewReader("")); err == nil {
		t.Error("closed stdin must abort the wizard")
	}
	if _, err := promptProxyConfig(strings.NewReader("\n\n")); err == nil {
		t.Error("closed stdin at host must abort the wizard")
	}
}

func TestPromptProxySchemePiped(t *testing.T) {
	cases := map[string]int{
		"\n":            0, // Enter = default http
		"1\n":           0,
		"2\n":           1,
		"3\n":           2,
		"socks5\n":      1,
		"HTTPS\n":       2,
		"4\nhttp\n":     0, // custom slot, then valid text
		"xyz\nsocks5\n": 1, // invalid, retry, valid
	}
	for in, want := range cases {
		got, err := promptProxySchemePiped(strings.NewReader(in))
		if err != nil || got != want {
			t.Errorf("scheme(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	if _, err := promptProxySchemePiped(strings.NewReader("4\n")); err == nil {
		t.Error("closed reader on free-text path must error, not hang")
	}
}

// fakeSocks5Server speaks just enough server side for one handshake:
// sends method/reply selections, records what the client sent.

func fakeSocks5Server(t *testing.T, conn net.Conn, method byte, user, pass string, reply byte, got *[]byte) {
	t.Helper()
	defer conn.Close()
	buf := make([]byte, 512)
	n, err := conn.Read(buf) // greeting
	if err != nil {
		t.Errorf("greeting: %v", err)
		return
	}
	*got = append(*got, buf[:n]...)
	if _, err := conn.Write([]byte{0x05, method}); err != nil {
		t.Errorf("method: %v", err)
		return
	}
	if method == 0xFF {
		return // client must abort; nothing more to speak
	}
	if method == 0x02 {
		n, err := conn.Read(buf) // auth
		if err != nil {
			t.Errorf("auth: %v", err)
			return
		}
		*got = append(*got, buf[:n]...)
		ulen := int(buf[1])
		gu := string(buf[2 : 2+ulen])
		plen := int(buf[2+ulen])
		gp := string(buf[3+ulen : 3+ulen+plen])
		if gu != user || gp != pass {
			_, _ = conn.Write([]byte{0x01, 0x01})
			return
		}
		if _, err := conn.Write([]byte{0x01, 0x00}); err != nil {
			t.Errorf("auth ok: %v", err)
			return
		}
	}
	n, err = conn.Read(buf) // connect request
	if err != nil {
		t.Errorf("request: %v", err)
		return
	}
	*got = append(*got, buf[:n]...)
	_, _ = conn.Write([]byte{0x05, reply, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
}

// dialViaFakeProxy runs fakeSocks5Server on a loopback listener and
// dials target through it with the production x/net-based dialer. It
// returns the client conn (nil plus the error on failure) and the raw
// bytes the fake server saw, so tests assert the wire transcript.

func dialViaFakeProxy(t *testing.T, method byte, user, pass string, reply byte, target, cfgUser string, cfgPass []byte) (net.Conn, []byte, error) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	var got []byte
	done := make(chan struct{})
	go func() {
		defer close(done)
		c, err := ln.Accept()
		if err != nil {
			return
		}
		fakeSocks5Server(t, c, method, user, pass, reply, &got)
	}()
	_, port, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	p := &clientcore.ProxyConfig{Scheme: "socks5", Host: "127.0.0.1", Port: mustAtoi(t, port), User: cfgUser, Pass: cfgPass}
	conn, err := socks5NetDialer(p)(context.Background(), "tcp", target)
	<-done
	if err != nil {
		return nil, got, err
	}
	return conn, got, nil
}

func TestSocks5Handshake(t *testing.T) {
	conn, got, err := dialViaFakeProxy(t, 0x00, "", "", 0x00, "chat.example.com:443", "", nil)
	if err != nil {
		t.Fatalf("handshake: %v", err)
	}
	_ = conn.Close()
	// Domain target must travel as ATYP 0x03 with the raw hostname:
	// the proxy resolves it, nothing is looked up locally.
	found := false
	for i := 0; i+2 < len(got); i++ {
		if got[i] == 0x03 && int(got[i+1]) == len("chat.example.com") &&
			string(got[i+2:i+2+len("chat.example.com")]) == "chat.example.com" {
			found = true
		}
	}
	if !found {
		t.Errorf("hostname not sent as domain address: %x", got)
	}
}

func TestSocks5HandshakeAuth(t *testing.T) {
	conn, got, err := dialViaFakeProxy(t, 0x02, "u", "p", 0x00, "10.0.0.1:80", "u", []byte("p"))
	if err != nil {
		t.Fatalf("auth handshake: %v", err)
	}
	_ = conn.Close()
	if len(got) == 0 || got[0] != 0x05 {
		t.Errorf("client bytes: %x", got)
	}
}

func TestSocks5HandshakeRefused(t *testing.T) {
	if _, _, err := dialViaFakeProxy(t, 0xFF, "", "", 0x00, "h:80", "", nil); err == nil {
		t.Error("method 0xFF must fail")
	}

	if _, _, err := dialViaFakeProxy(t, 0x00, "", "", 0x05, "h:80", "", nil); err == nil {
		t.Error("non-zero reply must fail")
	}
}

func TestValidateProxyScheme(t *testing.T) {
	for _, s := range []string{"http", "socks5", "https", " SOCKS5 ", "2"} {
		if err := validateProxyScheme(s); err != nil {
			t.Errorf("validateProxyScheme(%q) = %v, want nil", s, err)
		}
	}
	for _, s := range []string{"", "ftp", "4"} {
		if err := validateProxyScheme(s); err == nil {
			t.Errorf("validateProxyScheme(%q) must fail", s)
		}
	}
}

func TestProxyFieldValidators(t *testing.T) {
	if err := nonEmptyLine("proxy.local"); err != nil {
		t.Errorf("nonEmptyLine(host) = %v", err)
	}
	if err := nonEmptyLine("   "); err == nil {
		t.Error("nonEmptyLine(blank) must fail")
	}
	for _, p := range []string{"1", "1080", "8080", "65535"} {
		if err := validPort(p); err != nil {
			t.Errorf("validPort(%q) = %v", p, err)
		}
	}
	for _, p := range []string{"", "0", "65536", "abc", "-1"} {
		if err := validPort(p); err == nil {
			t.Errorf("validPort(%q) must fail", p)
		}
	}
}

func TestSocks5HandshakeAuthFail(t *testing.T) {
	// Server expects u/p; client offers wrong secret.
	if _, _, err := dialViaFakeProxy(t, 0x02, "u", "p", 0x00, "h:80", "u", []byte("WRONG")); err == nil {
		t.Error("wrong proxy password must fail the handshake")
	}
}

// TestDialSocks5WS_AuthFail: end-to-end dial against a proxy that
// rejects the password must surface an error, not hang or proceed.

func TestDialSocks5WS_AuthFail(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				var got []byte
				fakeSocks5Server(t, c, 0x02, "u", "p", 0x00, &got)
			}()
		}
	}()
	_, port, _ := net.SplitHostPort(ln.Addr().String())
	p := &clientcore.ProxyConfig{Scheme: "socks5", Host: "127.0.0.1", Port: mustAtoi(t, port), User: "u", Pass: []byte("WRONG")}
	if _, _, err := dialSocks5WS("ws://127.0.0.1:1/ws", http.Header{}, p); err == nil {
		t.Error("dial through auth-rejecting proxy must fail")
	}
}

// TestDialSocks5WS_Unreachable: a dead proxy address must fail within
// the handshake timeout, never hang the dial.

func TestDialSocks5WS_Unreachable(t *testing.T) {
	p := &clientcore.ProxyConfig{Scheme: "socks5", Host: "127.0.0.1", Port: deadPort(t)}
	start := time.Now()
	if _, _, err := dialSocks5WS("ws://127.0.0.1:1/ws", http.Header{}, p); err == nil {
		t.Error("dial through dead proxy must fail")
	}
	if time.Since(start) > socks5DialTimeout+10*time.Second {
		t.Error("dead proxy dial exceeded its timeout budget")
	}
}

// deadPort reserves then releases a loopback port, so nothing listens.

func deadPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	_, port, _ := net.SplitHostPort(ln.Addr().String())
	_ = ln.Close()
	return mustAtoi(t, port)
}

func mustAtoi(t *testing.T, s string) int {
	t.Helper()
	n, err := strconv.Atoi(s)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// TestParseProxyURLWording pins the exact lines the client has always shown
// for a refused proxy URL. The core reports a reason and the offending part,
// so the wording lives here and must not drift.
func TestParseProxyURLWording(t *testing.T) {
	cases := map[string]string{
		"://bad":                          "proxy URL không hợp lệ",
		"":                                `proxy scheme không hỗ trợ: "" (dùng http/https/socks5)`,
		"ftp://proxy.local:21":            `proxy scheme không hỗ trợ: "ftp" (dùng http/https/socks5)`,
		"http://:8080":                    "proxy thiếu host",
		"http://proxy.local:99999":        `proxy port không hợp lệ: "99999"`,
		"http://proxy.local:abc":          "proxy URL không hợp lệ",
		"http://:secret@proxy.local:8080": "proxy có password nhưng thiếu username",
	}
	for raw, want := range cases {
		_, err := parseProxyURL(raw)
		if err == nil || err.Error() != want {
			t.Errorf("parseProxyURL(%q) err = %v, want %q", raw, err, want)
		}
	}
	cfg, err := parseProxyURL("socks5://127.0.0.1:1080")
	if err != nil || cfg == nil || cfg.Port != 1080 {
		t.Fatalf("a valid URL must pass through: %+v, %v", cfg, err)
	}
}

//go:build !js

// Desktop-only: the wizard and resolve live in proxy_other.go, and TTY
// branches need terminal detection.
package main

import (
	"net"
	"strings"
	"testing"

	"github.com/CleveTok3125/V2V/identity"
)

func TestParseProxyURL(t *testing.T) {
	cfg, err := parseProxyURL("socks5://127.0.0.1:1080")
	if err != nil || cfg.Scheme != "socks5" || cfg.Host != "127.0.0.1" || cfg.Port != 1080 {
		t.Errorf("socks5 = %+v, %v", cfg, err)
	}
	cfg, err = parseProxyURL("http://user:secret@proxy.local")
	if err != nil || cfg.Port != 8080 || cfg.User != "user" || string(cfg.Pass) != "secret" {
		t.Errorf("http default port/auth = %+v, %v", cfg, err)
	}
	cfg, err = parseProxyURL("socks5h://proxy.local:1080")
	if err != nil || cfg.Scheme != "socks5" {
		t.Errorf("socks5h alias = %+v, %v", cfg, err)
	}
	for _, raw := range []string{
		"ftp://proxy.local:21",
		"http://:8080",
		"http://proxy.local:99999",
		"http://proxy.local:abc",
		"://bad",
		"",
	} {
		if _, err := parseProxyURL(raw); err == nil {
			t.Errorf("parseProxyURL(%q) must fail", raw)
		}
	}
}

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

func TestProxyLogStringMasksPassword(t *testing.T) {
	s := (&proxyConfig{Scheme: "http", Host: "h", Port: 8080, User: "u", Pass: []byte("secret")}).logString()
	if strings.Contains(s, "secret") || !strings.Contains(s, "u:***@h:8080") {
		t.Errorf("password must be masked: %q", s)
	}
	s = (&proxyConfig{Scheme: "socks5", Host: "h", Port: 1080}).logString()
	if s != "socks5://h:1080" {
		t.Errorf("no-auth form: %q", s)
	}
}

func TestProxyWipe(t *testing.T) {
	cfg, err := parseProxyURL("http://u:p@h:8080")
	if err != nil {
		t.Fatal(err)
	}
	cfg.wipe()
	if cfg.Pass != nil {
		t.Error("wipe must drop the password bytes")
	}
	if cfg.User != "u" || cfg.Host != "h" {
		t.Errorf("wipe must keep non-sensitive fields: %+v", cfg)
	}
	var nilCfg *proxyConfig
	nilCfg.wipe() // must not panic
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
		"\n":           0, // Enter = default http
		"1\n":          0,
		"2\n":          1,
		"3\n":          2,
		"socks5\n":     1,
		"HTTPS\n":      2,
		"4\nhttp\n":    0, // custom slot, then valid text
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

func TestSocks5Handshake(t *testing.T) {
	client, server := net.Pipe()
	var got []byte
	done := make(chan struct{})
	go func() {
		defer close(done)
		fakeSocks5Server(t, server, 0x00, "", "", 0x00, &got)
	}()
	if err := socks5Handshake(client, "chat.example.com", 443, nil, nil); err != nil {
		t.Fatalf("handshake: %v", err)
	}
	_ = client.Close()
	<-done
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
	client, server := net.Pipe()
	var got []byte
	done := make(chan struct{})
	go func() {
		defer close(done)
		fakeSocks5Server(t, server, 0x02, "u", "p", 0x00, &got)
	}()
	if err := socks5Handshake(client, "10.0.0.1", 80, []byte("u"), []byte("p")); err != nil {
		t.Fatalf("auth handshake: %v", err)
	}
	_ = client.Close()
	<-done
	if len(got) == 0 || got[0] != 0x05 {
		t.Errorf("client bytes: %x", got)
	}
}

func TestSocks5HandshakeRefused(t *testing.T) {
	client, server := net.Pipe()
	var got []byte
	done := make(chan struct{})
	go func() {
		defer close(done)
		fakeSocks5Server(t, server, 0xFF, "", "", 0x00, &got)
	}()
	if err := socks5Handshake(client, "h", 80, nil, nil); err == nil {
		t.Error("method 0xFF must fail")
	}
	_ = client.Close()
	<-done

	client2, server2 := net.Pipe()
	var got2 []byte
	done2 := make(chan struct{})
	go func() {
		defer close(done2)
		fakeSocks5Server(t, server2, 0x00, "", "", 0x05, &got2)
	}()
	if err := socks5Handshake(client2, "h", 80, nil, nil); err == nil {
		t.Error("non-zero reply must fail")
	}
	_ = client2.Close()
	<-done2
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

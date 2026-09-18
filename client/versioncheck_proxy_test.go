//go:build !js

package main

import (
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// pinProxyCache sets the shared proxy resolution outcome for one test.
func pinProxyCache(p *proxyConfig, err error) func() {
	oldResolved, oldProxy, oldErr := proxyResolved, resolvedProxy, proxyResolveErr
	proxyResolved, resolvedProxy, proxyResolveErr = true, p, err
	return func() { proxyResolved, resolvedProxy, proxyResolveErr = oldResolved, oldProxy, oldErr }
}

func TestVersionHTTPClientDirect(t *testing.T) {
	defer pinProxyCache(nil, nil)()
	c, err := versionHTTPClient()
	if err != nil {
		t.Fatal(err)
	}
	if c.Timeout != 5*time.Second {
		t.Fatalf("direct timeout = %v, want 5s", c.Timeout)
	}
	if c.Transport != nil {
		t.Fatal("direct must use the default transport")
	}
}

func TestVersionHTTPClientSocks(t *testing.T) {
	defer pinProxyCache(&proxyConfig{Scheme: "socks5", Host: "127.0.0.1", Port: 9050}, nil)()
	c, err := versionHTTPClient()
	if err != nil {
		t.Fatal(err)
	}
	if c.Timeout != 30*time.Second {
		t.Fatalf("proxied timeout = %v, want 30s", c.Timeout)
	}
	tr, ok := c.Transport.(*http.Transport)
	if !ok || tr.DialContext == nil {
		t.Fatal("socks5 must set a Transport with DialContext")
	}
}

func TestVersionHTTPClientHTTPProxy(t *testing.T) {
	defer pinProxyCache(&proxyConfig{Scheme: "http", Host: "127.0.0.1", Port: 8080}, nil)()
	c, err := versionHTTPClient()
	if err != nil {
		t.Fatal(err)
	}
	if c.Timeout != 30*time.Second {
		t.Fatalf("proxied timeout = %v, want 30s", c.Timeout)
	}
	tr, ok := c.Transport.(*http.Transport)
	if !ok || tr.Proxy == nil {
		t.Fatal("http proxy must set Transport.Proxy")
	}
}

func TestVersionHTTPClientResolveErr(t *testing.T) {
	defer pinProxyCache(nil, errors.New("wizard aborted"))()
	if _, err := versionHTTPClient(); err == nil {
		t.Fatal("resolve error must propagate")
	}
}

// relaySocks5 serves one SOCKS5 no-auth handshake on ln, relays the
// resulting stream to target, and reports when the relay ends.
func relaySocks5(t *testing.T, ln net.Listener, target string, done chan struct{}) {
	t.Helper()
	defer close(done)
	conn, err := ln.Accept()
	if err != nil {
		return
	}
	defer conn.Close()
	buf := make([]byte, 512)
	if _, err := io.ReadFull(conn, buf[:2]); err != nil { // greeting
		t.Errorf("greeting: %v", err)
		return
	}
	nmethods := int(buf[1])
	if _, err := io.ReadFull(conn, buf[:nmethods]); err != nil {
		t.Errorf("methods: %v", err)
		return
	}
	if _, err := conn.Write([]byte{0x05, 0x00}); err != nil { // no auth
		t.Errorf("method select: %v", err)
		return
	}
	hdr := make([]byte, 4)
	if _, err := io.ReadFull(conn, hdr); err != nil { // VER CMD RSV ATYP
		t.Errorf("request head: %v", err)
		return
	}
	var addr string
	switch hdr[3] {
	case 0x01:
		ip := make([]byte, 4)
		if _, err := io.ReadFull(conn, ip); err != nil {
			t.Errorf("ipv4: %v", err)
			return
		}
		addr = net.IP(ip).String()
	case 0x03:
		lenBuf := make([]byte, 1)
		if _, err := io.ReadFull(conn, lenBuf); err != nil {
			t.Errorf("domain len: %v", err)
			return
		}
		host := make([]byte, int(lenBuf[0]))
		if _, err := io.ReadFull(conn, host); err != nil {
			t.Errorf("domain: %v", err)
			return
		}
		addr = string(host)
	default:
		t.Errorf("unsupported ATYP %d", hdr[3])
		return
	}
	portBuf := make([]byte, 2)
	if _, err := io.ReadFull(conn, portBuf); err != nil {
		t.Errorf("port: %v", err)
		return
	}
	_ = addr // the relay target is fixed: tests assert reachability, not routing
	if _, err := conn.Write([]byte{0x05, 0x00, 0x00, 0x01, 0, 0, 0, 0, 0, 0}); err != nil {
		t.Errorf("reply: %v", err)
		return
	}
	up, err := net.Dial("tcp", target)
	if err != nil {
		t.Errorf("relay dial: %v", err)
		return
	}
	defer up.Close()
	go func() {
		_, _ = io.Copy(up, conn)
		// Client is done: no more requests will arrive. Shut down
		// the server side so the opposite copy stops waiting on
		// HTTP keep-alive instead of hanging the test.
		up.Close()
	}()
	_, _ = io.Copy(conn, up)
}

func TestVersionFetchViaSocks(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"version":"s9"}`))
	}))
	defer srv.Close()
	_, port, err := net.SplitHostPort(srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	target := net.JoinHostPort("127.0.0.1", port)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	done := make(chan struct{})
	go relaySocks5(t, ln, target, done)
	_, proxyPort, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer pinProxyCache(&proxyConfig{Scheme: "socks5", Host: "127.0.0.1", Port: mustAtoi(t, proxyPort)}, nil)()
	c, err := versionHTTPClient()
	if err != nil {
		t.Fatal(err)
	}
	got := fetchServerVersionVia(srv.URL, c)
	// The transport keeps the tunnel idle (keep-alive); close it so
	// both relay directions see EOF and done fires.
	c.CloseIdleConnections()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("relay did not finish after the fetch")
	}
	if got != "s9" {
		t.Fatalf("version via socks = %q, want s9", got)
	}
}

func TestVersionFetchViaSocksRefused(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"version":"s9"}`))
	}))
	defer srv.Close()
	defer pinProxyCache(&proxyConfig{Scheme: "socks5", Host: "127.0.0.1", Port: deadPort(t)}, nil)()
	c, err := versionHTTPClient()
	if err != nil {
		t.Fatal(err)
	}
	if got := fetchServerVersionVia(srv.URL, c); got != "" {
		t.Fatalf("dead proxy must yield unknown, got %q", got)
	}
}

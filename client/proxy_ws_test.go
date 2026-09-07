//go:build !js

package main

import (
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// fakeSocksProxy is a real TCP loopback SOCKS5 server for tests: it
// handshakes one connection, dials the requested target itself, and
// pipes bytes both ways. Addr returns the proxy address.
type fakeSocksProxy struct {
	t   *testing.T
	ln  net.Listener
	got string // target host requested, for assertions
}

func startFakeSocksProxy(t *testing.T) *fakeSocksProxy {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	fp := &fakeSocksProxy{t: t, ln: ln}
	go fp.serve()
	t.Cleanup(func() { _ = ln.Close() })
	return fp
}

func (fp *fakeSocksProxy) serve() {
	for {
		c, err := fp.ln.Accept()
		if err != nil {
			return
		}
		go fp.handle(c)
	}
}

func (fp *fakeSocksProxy) handle(client net.Conn) {
	defer client.Close()
	buf := make([]byte, 512)
	// Greeting: version + methods, answer no-auth.
	n, err := io.ReadAtLeast(client, buf[:2], 2)
	if err != nil || buf[0] != 0x05 {
		return
	}
	if _, err := io.ReadFull(client, buf[2:2+int(buf[1])]); err != nil {
		return
	}
	_ = n
	if _, err := client.Write([]byte{0x05, 0x00}); err != nil {
		return
	}
	// CONNECT request.
	head := make([]byte, 4)
	if _, err := io.ReadFull(client, head); err != nil {
		return
	}
	var host string
	var port int
	switch head[3] {
	case 0x01:
		raw := make([]byte, 6)
		if _, err := io.ReadFull(client, raw); err != nil {
			return
		}
		host = net.IP(raw[:4]).String()
		port = int(raw[4])<<8 | int(raw[5])
	case 0x03:
		ln := make([]byte, 1)
		if _, err := io.ReadFull(client, ln); err != nil {
			return
		}
		raw := make([]byte, int(ln[0])+2)
		if _, err := io.ReadFull(client, raw); err != nil {
			return
		}
		host = string(raw[:len(raw)-2])
		port = int(raw[len(raw)-2])<<8 | int(raw[len(raw)-1])
	default:
		return
	}
	fp.got = host
	target, err := net.DialTimeout("tcp", net.JoinHostPort(host, strconv.Itoa(port)), 5*time.Second)
	if err != nil {
		_, _ = client.Write([]byte{0x05, 0x05, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
		return
	}
	defer target.Close()
	_, _ = client.Write([]byte{0x05, 0x00, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
	go io.Copy(target, client)
	io.Copy(client, target)
}

// echoWS upgrades and echoes one message back, then closes.
func echoWS(t *testing.T) *httptest.Server {
	t.Helper()
	up := websocket.Upgrader{}
	return httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()
		mt, msg, err := c.ReadMessage()
		if err != nil {
			return
		}
		_ = c.WriteMessage(mt, msg)
	}))
}

// TestSocks5WSSingleTLS dials wss:// through the fake proxy and
// echoes a message. Before the double-TLS fix this failed with
// "tls: first record does not look like a TLS handshake".
func TestSocks5WSSingleTLS(t *testing.T) {
	target := echoWS(t)
	defer target.Close()
	targetAddr := strings.TrimPrefix(target.URL, "https://")

	proxy := startFakeSocksProxy(t)
	p := &proxyConfig{Scheme: "socks5", Host: "127.0.0.1", Port: proxyPort(t, proxy)}
	headers := http.Header{}
	d := websocket.Dialer{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, HandshakeTimeout: 10 * time.Second}
	conn, _, err := dialSocks5WSWithDialer("wss://localhost:"+targetPort(targetAddr)+"/ws", headers, p, d)
	if err != nil {
		t.Fatalf("wss through proxy: %v", err)
	}
	defer conn.Close()
	c := conn.(*websocket.Conn)
	if err := c.WriteMessage(websocket.TextMessage, []byte("ping")); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, msg, err := c.ReadMessage()
	if err != nil || string(msg) != "ping" {
		t.Fatalf("echo = %q, %v", msg, err)
	}
	if proxy.got != "localhost" {
		t.Errorf("proxy saw target host %q, want localhost (domain, unresolved locally)", proxy.got)
	}
	if p.Pass != nil {
		t.Errorf("password must wipe after dial")
	}
}

func proxyPort(t *testing.T, fp *fakeSocksProxy) int {
	t.Helper()
	_, port, err := net.SplitHostPort(fp.ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	var n int
	for _, c := range port {
		n = n*10 + int(c-'0')
	}
	return n
}

func targetPort(addr string) string {
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return ""
	}
	return port
}

//go:build !js

package main

import (
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/CleveTok3125/V2V/lib/clientcore"
)

// TestGateProbeGoesThroughTheConfiguredProxy: the join gate is reached
// before the chat session is dialled, so these requests do not go through
// the socket and the socket's proxy does not carry them. A bare client here
// connected from the real address, which is the worst place for it: the
// request body is the proof of work the address is being judged on.
//
// The relay is a real SOCKS5 server, so a pass means the bytes actually
// traversed the tunnel rather than that a Transport was merely configured.
func TestGateProbeGoesThroughTheConfiguredProxy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"required":true}`))
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
	// The relay only closes its done channel when the tunnel ends, and the
	// client keeps it alive, so the proof of routing has to be the accept.
	accepted := &observingListener{Listener: ln, hit: make(chan struct{}, 1)}
	done := make(chan struct{})
	go relaySocks5(t, accepted, target, done)
	_, proxyPort, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer pinProxyCache(&clientcore.ProxyConfig{Scheme: "socks5", Host: "127.0.0.1", Port: mustAtoi(t, proxyPort)}, nil)()

	if !clientcore.GateRequiredNow(srv.URL, gateConfig("")) {
		t.Fatal("the gate probe must reach the server through the proxy")
	}

	select {
	case <-accepted.hit:
	case <-time.After(5 * time.Second):
		t.Fatal("the request never reached the configured proxy")
	}
}

// observingListener reports the first accepted connection, so a test can wait
// for a dial without waiting for the tunnel to close.
type observingListener struct {
	net.Listener
	hit chan struct{}
}

func (l *observingListener) Accept() (net.Conn, error) {
	conn, err := l.Listener.Accept()
	if err == nil {
		select {
		case l.hit <- struct{}{}:
		default:
		}
	}
	return conn, err
}

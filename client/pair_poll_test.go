//go:build !js

package main

// The poll replaces what used to be a socket held open for the whole
// ceremony, so these tests are about the two properties that matter: the
// answer arrives without anything being held, and every outcome is
// explained to the person waiting.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/CleveTok3125/V2V/lib/clientcore"

	"github.com/gorilla/websocket"
)

// statusServer answers /pair/status with a scripted state.
func statusServer(t *testing.T, state func(call int32) (int, string)) *httptest.Server {
	t.Helper()
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/pair/status" {
			http.NotFound(w, r)
			return
		}
		if r.URL.Query().Get("ticket") == "" {
			http.Error(w, "missing ticket", http.StatusBadRequest)
			return
		}
		code, body := state(atomic.AddInt32(&calls, 1))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestPollPairStatusStopsWhenProved: the desktop must come back the moment
// the server says a browser proved the ticket. Announcing it is the caller's
// job, because the caller is what wipes the link first.
func TestPollPairStatusStopsWhenProved(t *testing.T) {
	srv := statusServer(t, func(int32) (int, string) { return http.StatusOK, `{"state":"verified"}` })
	if !pollPairStatus(srv.URL, "ticket", 300) {
		t.Fatal("a proved ticket was not reported")
	}
}

// TestPollPairStatusKeepsAskingWhilePending: "not yet" must not end the
// wait, and must not be reported as a failure.
func TestPollPairStatusKeepsAskingWhilePending(t *testing.T) {
	srv := statusServer(t, func(call int32) (int, string) {
		if call < 2 {
			return http.StatusOK, `{"state":"pending"}`
		}
		return http.StatusOK, `{"state":"verified"}`
	})
	if !pollPairStatus(srv.URL, "ticket", 300) {
		t.Fatal("the second answer did not end the wait")
	}
}

// TestPollPairStatusReportsAnExpiredLink: a link that is gone must say so
// rather than leave the person waiting out the whole budget.
func TestPollPairStatusReportsAnExpiredLink(t *testing.T) {
	srv := statusServer(t, func(int32) (int, string) {
		return http.StatusGone, "unknown or expired pairing"
	})
	out := captureStdout(t, func() {
		if pollPairStatus(srv.URL, "ticket", 300) {
			t.Error("an expired link was reported as proved")
		}
	})
	if !strings.Contains(out, "hết hạn") {
		t.Errorf("the expiry was not explained:\n%s", out)
	}
}

// TestPollPairStatusSurvivesAFailedPoll: a dropped request is not a failed
// ceremony. The desktop must keep asking instead of giving up on the first
// network hiccup.
func TestPollPairStatusSurvivesAFailedPoll(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&calls, 1) == 1 {
			// Close the connection without answering.
			hj, ok := w.(http.Hijacker)
			if !ok {
				return
			}
			conn, _, err := hj.Hijack()
			if err == nil {
				conn.Close()
			}
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"state":"verified"}`))
	}))
	t.Cleanup(srv.Close)

	if !pollPairStatus(srv.URL, "ticket", 300) {
		t.Fatal("a dropped poll ended the wait")
	}
}

// TestPollPairStatusGivesUpAtTheBudget: someone who never opens the link
// must be told, not left forever.
func TestPollPairStatusGivesUpAtTheBudget(t *testing.T) {
	srv := statusServer(t, func(int32) (int, string) { return http.StatusOK, `{"state":"pending"}` })
	out := captureStdout(t, func() {
		if pollPairStatus(srv.URL, "ticket", 1) {
			t.Error("a budget that ran out was reported as proved")
		}
	})
	if !strings.Contains(out, "Hết thời gian") {
		t.Errorf("the timeout was not explained:\n%s", out)
	}
}

// TestReadAuthResultReadsOneVerdict: the pairing wait no longer happens on
// the chat connection, so the handshake answers with exactly one frame.
func TestReadAuthResultReadsOneVerdict(t *testing.T) {
	sess := scriptedSocket(t, AuthPacket{Type: "auth_success", Username: "A#1", AuthType: "passkey"})
	got, err := readAuthResult(sess)
	if err != nil {
		t.Fatalf("readAuthResult: %v", err)
	}
	if got.Type != "auth_success" || got.AuthType != "passkey" {
		t.Fatalf("verdict = %+v", got)
	}
}

// TestReadAuthResultReportsAClosedSocket: a server that hangs up must
// surface as an error rather than a zero packet that reads like a refusal.
func TestReadAuthResultReportsAClosedSocket(t *testing.T) {
	sess := scriptedClosedSocket(t)
	if _, err := readAuthResult(sess); err == nil {
		t.Fatal("a closed socket reported success")
	}
}

// TestPairRequestCarriesOnlyTheRole: the ask is a role name and nothing
// else. It goes over HTTP now, so the assertion is on the body the desktop
// actually posts.
func TestPairRequestCarriesOnlyTheRole(t *testing.T) {
	raw, err := json.Marshal(pairRequest{Role: "member"})
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != `{"role":"member"}` {
		t.Fatalf("pair request body = %s, want only the role", raw)
	}
}

// TestValidateAuthFlagsRefusesTwoCredentials: -P owns the pre-auth exchange
// and -K owns the packet body; naming both leaves the role ambiguous.
func TestValidateAuthFlagsRefusesTwoCredentials(t *testing.T) {
	savedPasskey, savedKey := CLI.Passkey, CLI.KeyFile
	t.Cleanup(func() { CLI.Passkey, CLI.KeyFile = savedPasskey, savedKey })

	CLI.Passkey, CLI.KeyFile = "member", "/nonexistent/key.json"
	if err := validateAuthFlags(); err == nil {
		t.Fatal("passkey and key-file together were accepted")
	}
	CLI.KeyFile = ""
	if err := validateAuthFlags(); err != nil {
		t.Fatalf("passkey alone refused: %v", err)
	}
	CLI.Passkey, CLI.KeyFile = "", "/nonexistent/key.json"
	if err := validateAuthFlags(); err != nil {
		t.Fatalf("key-file alone refused: %v", err)
	}
}

// scriptedSocket hands the client a fixed sequence of frames, then holds
// the connection open so a client that stops reading fails rather than
// seeing EOF.
func scriptedSocket(t *testing.T, frames ...AuthPacket) *Session {
	t.Helper()
	up := websocket.Upgrader{}
	ready := make(chan *websocket.Conn, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		ready <- conn
	}))
	t.Cleanup(srv.Close)

	client, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { client.Close() })

	server := <-ready
	t.Cleanup(func() { server.Close() })
	go func() {
		for _, f := range frames {
			if err := server.WriteJSON(f); err != nil {
				return
			}
		}
		for {
			if _, _, err := server.ReadMessage(); err != nil {
				return
			}
		}
	}()

	return &Session{Conn: client}
}

// scriptedClosedSocket gives the client a server that hangs up at once.
func scriptedClosedSocket(t *testing.T) *Session {
	t.Helper()
	up := websocket.Upgrader{}
	ready := make(chan *websocket.Conn, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		ready <- conn
		conn.Close()
	}))
	t.Cleanup(srv.Close)

	client, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { client.Close() })
	<-ready
	return &Session{Conn: client}
}

// TestPollGoesThroughTheConfiguredProxy is the leak this pins: the poll used
// a bare http.Client, whose nil Transport falls back to
// http.DefaultTransport and therefore to the HTTP_PROXY environment
// variables. The session proxy from --proxy is never exported there, so a
// fresh client quietly connected from the real address while every other
// request in the pairing flow went through the proxy.
func TestPollGoesThroughTheConfiguredProxy(t *testing.T) {
	// The target must never be reached: reaching it means the poll went
	// direct.
	var targetHits atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		targetHits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"state":"pending"}`))
	}))
	t.Cleanup(target.Close)

	// The proxy answers on behalf of the target, so a routed poll finishes
	// on the first ask.
	var proxyHits atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxyHits.Add(1)
		if r.URL.Scheme == "" || r.URL.Host == "" {
			t.Errorf("proxy received a non-absolute request URI %q", r.URL)
			http.Error(w, "not a proxy request", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"state":"verified"}`))
	}))
	t.Cleanup(proxy.Close)

	host, port := splitHostPort(t, proxy.URL)
	savedProxy, savedResolved, savedErr := resolvedProxy, proxyResolved, proxyResolveErr
	t.Cleanup(func() {
		resolvedProxy, proxyResolved, proxyResolveErr = savedProxy, savedResolved, savedErr
	})
	resolvedProxy = &clientcore.ProxyConfig{Scheme: "http", Host: host, Port: port}
	proxyResolved = true
	proxyResolveErr = nil

	if !pollPairStatus(target.URL, "ticket", 5) {
		t.Fatal("the poll did not report a verified ceremony")
	}
	if proxyHits.Load() == 0 {
		t.Fatal("the poll bypassed the configured proxy")
	}
	if got := targetHits.Load(); got != 0 {
		t.Fatalf("the poll reached the target directly %d time(s)", got)
	}
}

// splitHostPort pulls the host and numeric port out of an httptest URL.
func splitHostPort(t *testing.T, raw string) (string, int) {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil {
		t.Fatal(err)
	}
	return u.Hostname(), port
}

//go:build !js

package main

// Before opening a browser the client asks whether this server can use
// passkeys at all. The answer is a convenience, never a gate: a server too
// old to have the endpoint must not be blocked by the client.

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// capabilitiesServer answers /api/capabilities with a scripted response.
func capabilitiesServer(t *testing.T, code int, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/capabilities" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestServerPasskeyEnabledReadsTheAnswer(t *testing.T) {
	for _, c := range []struct {
		body string
		want bool
	}{
		{`{"passkey":true}`, true},
		{`{"passkey":false}`, false},
	} {
		srv := capabilitiesServer(t, http.StatusOK, c.body)
		got, known := serverPasskeyEnabled(srv.URL)
		if !known {
			t.Errorf("%s: answer was discarded", c.body)
			continue
		}
		if got != c.want {
			t.Errorf("%s: got %v, want %v", c.body, got, c.want)
		}
	}
}

// TestServerPasskeyEnabledAdmitsIgnorance is the compatibility rule. An old
// server, a proxy, or a truncated answer all mean "the client does not
// know", and the handshake is the place that decides — not here.
func TestServerPasskeyEnabledAdmitsIgnorance(t *testing.T) {
	cases := []struct {
		name string
		code int
		body string
	}{
		{"old server", http.StatusNotFound, `404 page not found`},
		{"server error", http.StatusInternalServerError, `boom`},
		{"not json", http.StatusOK, `<html>nope</html>`},
		{"missing field", http.StatusOK, `{}`},
		{"wrong type", http.StatusOK, `{"passkey":"false"}`},
		{"empty", http.StatusOK, ``},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv := capabilitiesServer(t, c.code, c.body)
			got, known := serverPasskeyEnabled(srv.URL)
			if known {
				t.Fatalf("%s: claimed to know (passkey=%v); the handshake decides", c.name, got)
			}
		})
	}
}

// TestServerPasskeyEnabledSurvivesAnUnreachableServer: a dead endpoint is
// the same as an unknown answer, not a crash and not a block.
func TestServerPasskeyEnabledSurvivesAnUnreachableServer(t *testing.T) {
	srv := capabilitiesServer(t, http.StatusOK, `{"passkey":false}`)
	url := srv.URL
	srv.Close()

	if got, known := serverPasskeyEnabled(url); known {
		t.Fatalf("a dead server reported passkey=%v as known", got)
	}
}

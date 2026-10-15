package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// withAllowedOrigins swaps the CORS allow-list for the duration of a test.
func withAllowedOrigins(t *testing.T, origins ...string) {
	t.Helper()
	old := Cfg.Static.AllowedOrigins
	Cfg.Static.AllowedOrigins = origins
	t.Cleanup(func() { Cfg.Static.AllowedOrigins = old })
}

func TestCorsMiddlewareAllowsListedOrigin(t *testing.T) {
	withAllowedOrigins(t, "http://127.0.0.1:8080", " https://chat.example.com ")
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Origin") == "http://127.0.0.1:8080" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"required":true}`))
			return
		}
		w.WriteHeader(http.StatusTeapot)
	})
	h := corsMiddleware(next)

	req := httptest.NewRequest(http.MethodPost, "/api/join-gate", nil)
	req.Header.Set("Origin", "http://127.0.0.1:8080")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("allowed origin must reach the handler, got %d", rec.Code)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "http://127.0.0.1:8080" {
		t.Fatalf("Allow-Origin = %q", got)
	}
	if got := rec.Header().Get("Vary"); got != "Origin" {
		t.Fatalf("allowed response must vary by Origin, got %q", got)
	}
	// A whitespace-padded entry still matches.
	req = httptest.NewRequest(http.MethodPost, "/api/join-gate", nil)
	req.Header.Set("Origin", "https://chat.example.com")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "https://chat.example.com" {
		t.Fatalf("padded entry must match, got %q", got)
	}
}

func TestCorsMiddlewarePreflight(t *testing.T) {
	withAllowedOrigins(t, "http://127.0.0.1:8080")
	reached := false
	h := corsMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
	}))

	req := httptest.NewRequest(http.MethodOptions, "/api/join-gate", nil)
	req.Header.Set("Origin", "http://127.0.0.1:8080")
	req.Header.Set("Access-Control-Request-Method", "POST")
	req.Header.Set("Access-Control-Request-Headers", "content-type")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if reached {
		t.Fatal("preflight must not reach the handler")
	}
	if rec.Code != http.StatusNoContent {
		t.Fatalf("preflight status = %d", rec.Code)
	}
	checks := map[string]string{
		"Access-Control-Allow-Origin":  "http://127.0.0.1:8080",
		"Access-Control-Allow-Methods": "GET, POST, OPTIONS",
		"Access-Control-Allow-Headers": "Content-Type, X-V2V-Pass",
	}
	for header, want := range checks {
		if got := rec.Header().Get(header); got != want {
			t.Fatalf("%s = %q, want %q", header, got, want)
		}
	}
	if got := rec.Header().Get("Access-Control-Allow-Credentials"); got != "" {
		t.Fatalf("Allow-Credentials must stay unset, got %q", got)
	}
}

func TestCorsMiddlewareRejectsOtherOrigins(t *testing.T) {
	withAllowedOrigins(t, "http://127.0.0.1:8080")
	h := corsMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	for _, origin := range []string{"", "https://evil.example", "http://localhost:8080"} {
		req := httptest.NewRequest(http.MethodPost, "/api/join-gate", nil)
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
			t.Fatalf("origin %q must get no Allow-Origin, got %q", origin, got)
		}
		if rec.Code != http.StatusOK {
			t.Fatalf("origin %q must pass through, got %d", origin, rec.Code)
		}
	}
}

// A preflight from an origin outside the list is not answered by the
// middleware: the mux sees it and the browser gets no CORS approval.
func TestCorsMiddlewarePreflightOtherOriginPassesThrough(t *testing.T) {
	withAllowedOrigins(t, "http://127.0.0.1:8080")
	h := corsMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusMethodNotAllowed)
	}))

	req := httptest.NewRequest(http.MethodOptions, "/api/join-gate", nil)
	req.Header.Set("Origin", "https://evil.example")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want the handler's answer", rec.Code)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("Allow-Origin = %q", got)
	}
}

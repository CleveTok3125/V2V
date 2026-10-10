package main

// A refused dial tells the client how long to wait. The other 429 and 503
// paths already send Retry-After; the cooldown and the ban did not, so a
// person retrying by hand had to guess.

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"
)

// retryAfterSeconds reads the header and returns it as a number, failing the
// test when it is missing or not a positive integer.
func retryAfterSeconds(t *testing.T, rec *httptest.ResponseRecorder) int {
	t.Helper()
	raw := rec.Header().Get("Retry-After")
	if raw == "" {
		t.Fatal("no Retry-After on a 429")
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 {
		t.Fatalf("Retry-After = %q, want a positive integer of seconds", raw)
	}
	return n
}

// TestConnectionCooldownSaysHowLong: the dial that trips the cooldown must
// carry the remaining window, or the client cannot tell a five-second wait
// from a five-minute one.
func TestConnectionCooldownSaysHowLong(t *testing.T) {
	s := pairServer(t)
	dyn := Cfg.Dynamic.Load()
	dyn.ConnectionCooldown = 5 * time.Second

	if !s.CheckConnectionRate(httptest.NewRecorder(), "192.0.2.1") {
		t.Fatal("the first dial was refused")
	}
	rec := httptest.NewRecorder()
	if s.CheckConnectionRate(rec, "192.0.2.1") {
		t.Fatal("the second dial passed; the cooldown is not applied")
	}
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusTooManyRequests)
	}
	got := retryAfterSeconds(t, rec)
	if got > 5 {
		t.Fatalf("Retry-After = %d, want at most the 5s cooldown", got)
	}
}

// TestBanSaysHowLong: the ban message already names a clock time, so the
// header must agree with it rather than leaving the client to parse prose.
func TestBanSaysHowLong(t *testing.T) {
	s := pairServer(t)
	dyn := Cfg.Dynamic.Load()
	dyn.ConnectionCooldown = 0

	s.AuthFailsMu.Lock()
	s.AuthFails["192.0.2.2"] = RateLimitRecord{UnlockTime: time.Now().Add(90 * time.Second)}
	s.AuthFailsMu.Unlock()

	rec := httptest.NewRecorder()
	if s.CheckConnectionRate(rec, "192.0.2.2") {
		t.Fatal("a banned address was allowed in")
	}
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusTooManyRequests)
	}
	got := retryAfterSeconds(t, rec)
	if got < 80 || got > 90 {
		t.Fatalf("Retry-After = %d, want the remaining ~90s of the ban", got)
	}
}

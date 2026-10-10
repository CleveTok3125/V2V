package main

// A refused dial tells the client how long to wait. The other 429 and 503
// paths already send Retry-After; the cooldown and the ban did not, so a
// person retrying by hand had to guess.

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/CleveTok3125/V2V/internal/guard"
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

// TestHandlerCooldownsSayHowLong: every 429 the server raises should carry
// the wait. These three are unauthenticated and unread by the desktop, but
// their clients still retry, and a bare refusal makes them guess exactly as
// the connection cooldown used to.
func TestHandlerCooldownsSayHowLong(t *testing.T) {
	// These meters are package-level and keyed by client IP, and httptest
	// requests all arrive from the same one. Hand them back empty: a refusal
	// left here would greet the next test that touches the same handler.
	meters := []*guard.CooldownMap{
		enrollBeginCooldowns, enrollFinishCooldowns, pairStatusCooldowns,
	}
	for _, meter := range meters {
		meter.Reset()
		t.Cleanup(meter.Reset)
	}

	t.Run("pair status", func(t *testing.T) {
		s := pairServer(t)
		ticket := strings.Repeat("ab", pairTicketBytes)

		first := httptest.NewRecorder()
		s.handlePairStatus(first, httptest.NewRequest(http.MethodGet, "/pair/status?ticket="+ticket, nil))
		if first.Code == http.StatusTooManyRequests {
			t.Fatal("the first poll was metered")
		}
		rec := httptest.NewRecorder()
		s.handlePairStatus(rec, httptest.NewRequest(http.MethodGet, "/pair/status?ticket="+ticket, nil))
		if rec.Code != http.StatusTooManyRequests {
			t.Fatalf("second poll = %d, want %d", rec.Code, http.StatusTooManyRequests)
		}
		if got := retryAfterSeconds(t, rec); got > int(pairStatusCooldown.Seconds()) {
			t.Fatalf("Retry-After = %d, want at most the %v cooldown", got, pairStatusCooldown)
		}
	})

	t.Run("enroll begin", func(t *testing.T) {
		s := pairServer(t)
		// handleEnrollBegin prunes the store before it answers, and the
		// default store lives under the repository root: without this the
		// test writes webauthn.json into the working tree.
		s.WebAuthn = NewWebAuthnStore(t.TempDir() + "/webauthn.json")

		first := httptest.NewRecorder()
		s.handleEnrollBegin(first, httptest.NewRequest(http.MethodGet, "/webauthn/enroll/begin?ticket=x", nil))
		if first.Code == http.StatusTooManyRequests {
			t.Fatal("the first begin was metered")
		}
		rec := httptest.NewRecorder()
		s.handleEnrollBegin(rec, httptest.NewRequest(http.MethodGet, "/webauthn/enroll/begin?ticket=x", nil))
		if rec.Code != http.StatusTooManyRequests {
			t.Fatalf("second begin = %d, want %d", rec.Code, http.StatusTooManyRequests)
		}
		if got := retryAfterSeconds(t, rec); got > int(enrollBeginCooldown.Seconds()) {
			t.Fatalf("Retry-After = %d, want at most the %v cooldown", got, enrollBeginCooldown)
		}
	})

	t.Run("enroll finish", func(t *testing.T) {
		s := pairServer(t)
		s.WebAuthn = NewWebAuthnStore(t.TempDir() + "/webauthn.json")

		first := httptest.NewRecorder()
		s.handleEnrollFinish(first, httptest.NewRequest(http.MethodPost, "/webauthn/enroll/finish", strings.NewReader("{}")))
		if first.Code == http.StatusTooManyRequests {
			t.Fatal("the first finish was metered")
		}
		rec := httptest.NewRecorder()
		s.handleEnrollFinish(rec, httptest.NewRequest(http.MethodPost, "/webauthn/enroll/finish", strings.NewReader("{}")))
		if rec.Code != http.StatusTooManyRequests {
			t.Fatalf("second finish = %d, want %d", rec.Code, http.StatusTooManyRequests)
		}
		if got := retryAfterSeconds(t, rec); got > int(enrollFinishCooldown.Seconds()) {
			t.Fatalf("Retry-After = %d, want at most the %v cooldown", got, enrollFinishCooldown)
		}
	})
}

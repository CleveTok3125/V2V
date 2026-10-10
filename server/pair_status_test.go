package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func getPairStatus(s *ChatServer, from, ticket string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/pair/status?ticket="+ticket, nil)
	req.RemoteAddr = from + ":40000"
	s.handlePairStatus(rec, req)
	return rec
}

func statusState(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("status body is not JSON: %v (%s)", err, rec.Body)
	}
	state, _ := got["state"].(string)
	return state
}

// TestPairStatusPendingThenVerified is the desktop's whole view of the wait:
// it asks, gets "not yet" without holding anything, and later gets "proved".
func TestPairStatusPendingThenVerified(t *testing.T) {
	withPairCap(t, 16)
	s := pairServer(t)
	ticket, err := s.issuePairTicket("member", "198.51.100.4")
	if err != nil {
		t.Fatal(err)
	}

	rec := getPairStatus(s, "198.51.100.4", ticket)
	if rec.Code != http.StatusOK {
		t.Fatalf("pending poll = %d, want %d", rec.Code, http.StatusOK)
	}
	if state := statusState(t, rec); state != "pending" {
		t.Fatalf("state = %q, want pending", state)
	}

	if !s.markPairVerified(ticket) {
		t.Fatal("markPairVerified refused a live ticket")
	}
	pairStatusCooldowns.Reset()
	rec = getPairStatus(s, "198.51.100.4", ticket)
	if got := statusState(t, rec); got != "verified" {
		t.Fatalf("state = %q, want verified", got)
	}
}

// TestPairStatusRefusals: the poll must be useless to anyone but the address
// that asked, and must not describe why a ticket failed.
func TestPairStatusRefusals(t *testing.T) {
	withPairCap(t, 16)

	t.Run("unknown", func(t *testing.T) {
		s := pairServer(t)
		rec := getPairStatus(s, "198.51.100.4", strings.Repeat("ab", 64))
		if rec.Code != http.StatusGone {
			t.Fatalf("unknown ticket = %d, want %d", rec.Code, http.StatusGone)
		}
	})

	t.Run("another address", func(t *testing.T) {
		s := pairServer(t)
		ticket, err := s.issuePairTicket("member", "198.51.100.4")
		if err != nil {
			t.Fatal(err)
		}
		rec := getPairStatus(s, "203.0.113.9", ticket)
		if rec.Code != http.StatusGone {
			t.Fatalf("foreign poll = %d, want %d", rec.Code, http.StatusGone)
		}
	})

	t.Run("spent", func(t *testing.T) {
		s := pairServer(t)
		ticket, err := s.issuePairTicket("member", "198.51.100.4")
		if err != nil {
			t.Fatal(err)
		}
		s.markPairVerified(ticket)
		if _, err := s.takeVerifiedPairTicket(ticket, "198.51.100.4"); err != nil {
			t.Fatal("could not spend the ticket")
		}
		pairStatusCooldowns.Reset()
		rec := getPairStatus(s, "198.51.100.4", ticket)
		if rec.Code != http.StatusGone {
			t.Fatalf("spent ticket = %d, want %d", rec.Code, http.StatusGone)
		}
	})

	t.Run("wrong method", func(t *testing.T) {
		s := pairServer(t)
		rec := httptest.NewRecorder()
		s.handlePairStatus(rec, httptest.NewRequest(http.MethodPost, "/pair/status", nil))
		if rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("POST = %d, want %d", rec.Code, http.StatusMethodNotAllowed)
		}
	})

	t.Run("missing ticket", func(t *testing.T) {
		s := pairServer(t)
		rec := httptest.NewRecorder()
		s.handlePairStatus(rec, httptest.NewRequest(http.MethodGet, "/pair/status", nil))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("no ticket = %d, want %d", rec.Code, http.StatusBadRequest)
		}
	})
}

// TestPairStatusMeter: the endpoint is unauthenticated, so a caller spinning
// on it has to be refused while a desktop polling at its own cadence is not.
func TestPairStatusMeter(t *testing.T) {
	withPairCap(t, 16)
	s := pairServer(t)
	ticket, err := s.issuePairTicket("member", "198.51.100.4")
	if err != nil {
		t.Fatal(err)
	}
	pairStatusCooldowns.Reset()

	if rec := getPairStatus(s, "198.51.100.4", ticket); rec.Code == http.StatusTooManyRequests {
		t.Fatal("the first poll was refused")
	}
	if rec := getPairStatus(s, "198.51.100.4", ticket); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("second poll in the same second = %d, want %d", rec.Code, http.StatusTooManyRequests)
	}
}

package main

// Asking for a pairing ticket. This is HTTP rather than a websocket dial on
// purpose: a desktop logs in twice — once to ask, once to spend — and the
// connection cooldown refuses two dials from one address inside its window.
// Keeping the ask off the socket leaves the desktop exactly one dial.

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/CleveTok3125/V2V/internal/serverconfig"
)

func pairRequestBody(t *testing.T, role string) *bytes.Reader {
	t.Helper()
	raw, err := json.Marshal(map[string]string{"role": role})
	if err != nil {
		t.Fatal(err)
	}
	return bytes.NewReader(raw)
}

func postPairRequest(s *ChatServer, body *bytes.Reader) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/pair/request", body)
	s.handlePairRequest(rec, req)
	return rec
}

// TestPairRequestIssuesATicket is the whole endpoint: a role is named, and
// what comes back is a ticket bound to it plus the link that proves it.
func TestPairRequestIssuesATicket(t *testing.T) {
	s := pairServer(t)
	rec := postPairRequest(s, pairRequestBody(t, "admin"))
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d (%s)", rec.Code, http.StatusCreated, rec.Body)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}

	var offer struct {
		Ticket string `json:"ticket"`
		URL    string `json:"url"`
		TTLSec int    `json:"ttl_sec"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &offer); err != nil {
		t.Fatalf("body is not JSON: %v (%s)", err, rec.Body)
	}
	if len(offer.Ticket) != pairTicketBytes*2 {
		t.Fatalf("ticket = %q (%d chars), want %d hex chars", offer.Ticket, len(offer.Ticket), pairTicketBytes*2)
	}
	// The ticket rides in the fragment, which a browser never puts in a
	// request line, so it stays out of server and proxy logs.
	wantURL := WAConfig.Origin + "/web/#pair=" + offer.Ticket
	if offer.URL != wantURL {
		t.Errorf("url = %q, want %q", offer.URL, wantURL)
	}
	if offer.TTLSec != int(pairTicketTTL.Seconds()) {
		t.Errorf("ttl_sec = %d, want %d", offer.TTLSec, int(pairTicketTTL.Seconds()))
	}

	// The ticket must name this role and this address, so a leaked link is
	// useless to anyone else.
	ticket, err := s.pairTicketState(offer.Ticket, "192.0.2.1")
	if err != nil {
		t.Fatalf("issued ticket does not resolve for the caller: %v", err)
	}
	if ticket.Role != "admin" {
		t.Errorf("ticket role = %q, want admin", ticket.Role)
	}
	if ticket.Verified {
		t.Error("a fresh ticket must not be verified")
	}
}

// TestPairRequestRefusals covers the ways an ask must not mint anything.
func TestPairRequestRefusals(t *testing.T) {
	t.Run("wrong method", func(t *testing.T) {
		s := pairServer(t)
		rec := httptest.NewRecorder()
		s.handlePairRequest(rec, httptest.NewRequest(http.MethodGet, "/pair/request", nil))
		if rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("GET = %d, want %d", rec.Code, http.StatusMethodNotAllowed)
		}
	})

	t.Run("passkey disabled", func(t *testing.T) {
		s := pairServer(t)
		WAConfig.Enabled = false
		rec := postPairRequest(s, pairRequestBody(t, "member"))
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
		}
		if s.countPairTickets() != 0 {
			t.Error("a disabled server minted a ticket")
		}
	})

	t.Run("missing role", func(t *testing.T) {
		s := pairServer(t)
		rec := postPairRequest(s, pairRequestBody(t, ""))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
		}
	})

	t.Run("unknown role", func(t *testing.T) {
		s := pairServer(t)
		rec := postPairRequest(s, pairRequestBody(t, "nobody"))
		if rec.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
		}
		if s.countPairTickets() != 0 {
			t.Error("an unknown role minted a ticket")
		}
	})

	t.Run("malformed body", func(t *testing.T) {
		s := pairServer(t)
		rec := postPairRequest(s, bytes.NewReader([]byte("not json")))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
		}
	})

	t.Run("capacity", func(t *testing.T) {
		s := pairServer(t)
		// Fill the ceiling from other addresses so the per-address cooldown
		// is not what refuses the next caller.
		for i := 0; i < 4; i++ {
			if _, err := s.issuePairTicket("member", "198.51.100."+string(rune('a'+i))); err != nil {
				t.Fatalf("filling the store: %v", err)
			}
		}
		old := Cfg.Abuse.Load()
		t.Cleanup(func() { Cfg.Abuse.Store(old) })
		Cfg.Abuse.Store(&serverconfig.AbuseConfig{PairMaxPending: 4})
		rec := postPairRequest(s, pairRequestBody(t, "member"))
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("status = %d, want %d (full store must say so, not 500)", rec.Code, http.StatusServiceUnavailable)
		}
	})
}

// TestPairRequestIsMeteredPerAddress: the endpoint mints durable state, so a
// caller spinning on it must be refused rather than allowed to fill the
// pending ceiling from one address.
func TestPairRequestIsMeteredPerAddress(t *testing.T) {
	s := pairServer(t)
	pairRequestCooldowns.Reset()

	first := postPairRequest(s, pairRequestBody(t, "member"))
	if first.Code != http.StatusCreated {
		t.Fatalf("first ask = %d, want %d", first.Code, http.StatusCreated)
	}
	second := postPairRequest(s, pairRequestBody(t, "member"))
	if second.Code != http.StatusTooManyRequests {
		t.Fatalf("second ask = %d, want %d", second.Code, http.StatusTooManyRequests)
	}
	if got := s.countPairTickets(); got != 1 {
		t.Fatalf("store holds %d tickets, want 1", got)
	}
}

// TestAskingForATicketDoesNotSpendTheConnectionCooldown is the field
// regression. The desktop used to dial for the ticket and dial again to
// spend it; the second dial arrived inside CONNECTION_COOLDOWN and was
// refused as churn, so a person who had just proved a passkey was told they
// were connecting too fast. The ask is off the socket now, and this pins
// that it leaves the cooldown alone.
func TestAskingForATicketDoesNotSpendTheConnectionCooldown(t *testing.T) {
	s := pairServer(t)
	dyn := Cfg.Dynamic.Load()
	dyn.ConnectionCooldown = 5 * time.Second

	if rec := postPairRequest(s, pairRequestBody(t, "member")); rec.Code != http.StatusCreated {
		t.Fatalf("ask = %d, want %d", rec.Code, http.StatusCreated)
	}

	// The one dial the desktop now makes, right after the ask.
	const caller = "192.0.2.1"
	if !s.CheckConnectionRate(httptest.NewRecorder(), caller) {
		t.Fatal("the login dial was refused as churn immediately after asking for a ticket")
	}
	// The cooldown still exists: the dial above spent it.
	if s.CheckConnectionRate(httptest.NewRecorder(), caller) {
		t.Fatal("two dials passed back to back; the cooldown is not being applied")
	}
}

// TestPairRequestAnswersAMistakeWithItsOwnError: the meter bounds the store,
// so it must sit after the cheap checks. Metering first would answer a
// mistyped role with "too many attempts", which is both wrong and
// indistinguishable from the caller having done something abusive.
func TestPairRequestAnswersAMistakeWithItsOwnError(t *testing.T) {
	s := pairServer(t)
	pairRequestCooldowns.Reset()

	if rec := postPairRequest(s, pairRequestBody(t, "member")); rec.Code != http.StatusCreated {
		t.Fatalf("ask = %d, want %d", rec.Code, http.StatusCreated)
	}
	// Same meter window, but the role is wrong: the caller learns that.
	if rec := postPairRequest(s, pairRequestBody(t, "nobody")); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown role right after an ask = %d, want %d", rec.Code, http.StatusNotFound)
	}
	if rec := postPairRequest(s, pairRequestBody(t, "")); rec.Code != http.StatusBadRequest {
		t.Fatalf("missing role right after an ask = %d, want %d", rec.Code, http.StatusBadRequest)
	}
	// A valid ask in the same window is still metered.
	if rec := postPairRequest(s, pairRequestBody(t, "member")); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("second valid ask = %d, want %d", rec.Code, http.StatusTooManyRequests)
	}
}

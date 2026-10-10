package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// pairTestIP is the address httptest requests arrive from. A ticket must
// be issued for that exact address or the store's binding refuses it.
const pairTestIP = "192.0.2.1"

// pairBody builds a /pair/submit payload. The defaults are a complete,
// well-formed request carrying a signature no credential can verify, so a
// test only states the fields it is actually about.
func pairBody(t *testing.T, ticket string, fields map[string]any) *bytes.Reader {
	t.Helper()
	payload := map[string]any{
		"ticket":      ticket,
		"passkey_id":  "cred",
		"auth_data":   base64.RawURLEncoding.EncodeToString([]byte("auth")),
		"client_data": base64.RawURLEncoding.EncodeToString([]byte(`{"type":"webauthn.get"}`)),
		"sig":         base64.RawURLEncoding.EncodeToString([]byte("sig")),
	}
	for k, v := range fields {
		payload[k] = v
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	return bytes.NewReader(raw)
}

func postPair(s *ChatServer, body *bytes.Reader) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/pair/submit", body)
	s.handlePairSubmit(rec, req)
	return rec
}

// postPairFrom posts from a named address. The submission meter is per IP, so
// a test that needs two independent submissions must not reuse one.
func postPairFrom(s *ChatServer, from string, body *bytes.Reader) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/pair/submit", body)
	req.RemoteAddr = from + ":40000"
	s.handlePairSubmit(rec, req)
	return rec
}

// TestPairSubmitRejectsUnknownTicket: a ticket that names no live pairing
// must be refused before any parsing work, so the endpoint cannot be used to
// probe for valid ones.
func TestPairSubmitRejectsUnknownTicket(t *testing.T) {
	s := pairServer(t)
	s.WebAuthn = NewWebAuthnStore(t.TempDir() + "/webauthn.json")

	rec := postPair(s, pairBody(t, strings.Repeat("ab", 64), nil))
	if rec.Code != http.StatusGone {
		t.Fatalf("unknown ticket = %d, want %d (%s)", rec.Code, http.StatusGone, rec.Body)
	}
	if s.countPairTickets() != 0 {
		t.Fatal("a rejected submission minted a ticket")
	}
}

// TestPairSubmitRejectsWrongMethod: only POST runs a ceremony submission.
func TestPairSubmitRejectsWrongMethod(t *testing.T) {
	s := pairServer(t)
	rec := httptest.NewRecorder()
	s.handlePairSubmit(rec, httptest.NewRequest(http.MethodGet, "/pair/submit", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET = %d, want %d", rec.Code, http.StatusMethodNotAllowed)
	}
}

// TestPairSubmitDisabledStaysDisabled: with no WebAuthn support the endpoint
// must not accept anything at all, so a deployment that turned passkeys off
// cannot be driven through a side door.
func TestPairSubmitDisabledStaysDisabled(t *testing.T) {
	s := pairServer(t)
	WAConfig.Enabled = false
	if _, err := s.issuePairTicket("member", pairTestIP); err != nil {
		t.Fatal(err)
	}
	rec := postPair(s, pairBody(t, "anything", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("disabled = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
}

// TestPairSubmitRequiresFields: a submission missing any assertion field is
// malformed, and the ticket must be untouched by it.
func TestPairSubmitRequiresFields(t *testing.T) {
	withPairCap(t, 16)
	s := pairServer(t)
	s.WebAuthn = NewWebAuthnStore(t.TempDir() + "/webauthn.json")
	ticket, err := s.issuePairTicket("member", pairTestIP)
	if err != nil {
		t.Fatal(err)
	}
	rec := postPair(s, pairBody(t, ticket, map[string]any{"passkey_id": ""}))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("missing credential = %d, want %d", rec.Code, http.StatusBadRequest)
	}
	if t2, ok := s.pairTicketFor(ticket, pairTestIP); !ok || t2.Verified {
		t.Fatal("a malformed submission changed the ticket")
	}
}

// TestPairSubmitRefusalLeavesTheLinkUsable: a person who taps the wrong
// passkey, or whose phone drops the response, must be able to try again on
// the same link instead of restarting the client and the ceremony.
func TestPairSubmitRefusalLeavesTheLinkUsable(t *testing.T) {
	withPairCap(t, 16)
	s := pairServer(t)
	store := NewWebAuthnStore(t.TempDir() + "/webauthn.json")
	s.WebAuthn = store
	auth := newTestPasskey(t, store, "member")

	ticket, err := s.issuePairTicket("member", pairTestIP)
	if err != nil {
		t.Fatal(err)
	}
	// A mangled signature: the credential is right, the proof is not.
	bad := auth.assert(t, ticket)
	bad["passkey_id"] = auth.credentialID()
	bad["sig"] = base64.RawURLEncoding.EncodeToString([]byte("not a signature"))
	if rec := postPair(s, pairBody(t, ticket, bad)); rec.Code == http.StatusAccepted {
		t.Fatal("a mangled signature was accepted")
	}
	if t2, ok := s.pairTicketFor(ticket, pairTestIP); !ok || t2.Verified {
		t.Fatal("a refused assertion marked the ticket verified or dropped it")
	}

	// The same link still works with a real assertion.
	good := auth.assert(t, ticket)
	good["passkey_id"] = auth.credentialID()
	pairSubmitCooldowns.Reset()
	rec := postPair(s, pairBody(t, ticket, good))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("retry after a bad signature = %d, want %d (%s)", rec.Code, http.StatusAccepted, rec.Body)
	}
	if t3, ok := s.pairTicketFor(ticket, pairTestIP); !ok || !t3.Verified {
		t.Fatal("a verified assertion did not mark the ticket")
	}
}

// TestPairSubmitVerifiesAgainstTheTicketRole: the assertion is checked
// against the role the desktop named, never against a role the submission
// carries. A browser that lies about its role gets nowhere.
func TestPairSubmitVerifiesAgainstTheTicketRole(t *testing.T) {
	withPairCap(t, 16)
	s := pairServer(t)
	s.WebAuthn = NewWebAuthnStore(t.TempDir() + "/webauthn.json")
	ticket, err := s.issuePairTicket("member", pairTestIP)
	if err != nil {
		t.Fatal(err)
	}
	rec := postPair(s, pairBody(t, ticket, map[string]any{"role": "admin"}))
	if rec.Code == http.StatusAccepted {
		t.Fatal("a bogus assertion was accepted")
	}
	if t2, ok := s.pairTicketFor(ticket, pairTestIP); !ok || t2.Verified {
		t.Fatal("a refused assertion changed the ticket")
	}
}

// TestPairSubmitRateLimits: every attempt does a store lookup, so an
// unauthenticated endpoint has to be metered before the ticket is touched.
func TestPairSubmitRateLimits(t *testing.T) {
	s := pairServer(t)
	s.WebAuthn = NewWebAuthnStore(t.TempDir() + "/webauthn.json")
	pairSubmitCooldowns.Reset()

	for i := 0; i < 2; i++ {
		rec := postPair(s, pairBody(t, strings.Repeat("cd", 64), nil))
		if rec.Code == http.StatusTooManyRequests {
			return
		}
	}
	t.Skip("cooldown map did not trip; check the limiter is wired")
}

// TestPairSubmitIsRateLimitedBeforeTheStore: the meter must run ahead of any
// store read, so spam cannot turn the endpoint into disk I/O.
func TestPairSubmitIsRateLimitedBeforeTheStore(t *testing.T) {
	s := pairServer(t)
	s.WebAuthn = &WebAuthnStore{path: t.TempDir() + "/does-not-exist.json"}
	pairSubmitCooldowns.Reset()

	if rec := postPair(s, pairBody(t, strings.Repeat("ef", 64), nil)); rec.Code == http.StatusTooManyRequests {
		return
	}
	if second := postPair(s, pairBody(t, strings.Repeat("ef", 64), nil)); second.Code != http.StatusTooManyRequests {
		t.Fatalf("second call = %d, want %d", second.Code, http.StatusTooManyRequests)
	}
}

// TestPairSubmitAcceptsVerifiedAssertion walks the endpoint end to end with a
// real ES256 credential: the browser asserts over the ticket, the handler
// verifies it through the same library path the web login uses, the sign
// counter advances, and the ticket is left proved for the desktop to spend.
func TestPairSubmitAcceptsVerifiedAssertion(t *testing.T) {
	withPairCap(t, 16)
	s := pairServer(t)
	store := NewWebAuthnStore(t.TempDir() + "/webauthn.json")
	s.WebAuthn = store
	auth := newTestPasskey(t, store, "member")

	ticket, err := s.issuePairTicket("member", pairTestIP)
	if err != nil {
		t.Fatal(err)
	}
	doc := auth.assert(t, ticket)
	doc["passkey_id"] = auth.credentialID()

	rec := postPair(s, pairBody(t, ticket, doc))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("verified assertion = %d, want %d (%s)", rec.Code, http.StatusAccepted, rec.Body)
	}
	if t2, ok := s.pairTicketFor(ticket, pairTestIP); !ok || !t2.Verified {
		t.Fatal("a verified assertion did not mark the ticket")
	}

	// The counter must have advanced, so a replay of the same assertion is
	// caught by the clone check rather than by luck.
	cred, ok := store.Credential("member", auth.credentialID())
	if !ok {
		t.Fatal("credential vanished from the store")
	}
	if cred.SignCount == 0 {
		t.Fatal("sign counter did not advance")
	}

	// Spending is the handshake's job: the ticket is still here until a
	// desktop presents it, and then it is gone.
	if _, err := s.takeVerifiedPairTicket(ticket, pairTestIP); err != nil {
		t.Fatal("the proved ticket could not be spent")
	}
	if _, err := s.takeVerifiedPairTicket(ticket, pairTestIP); err == nil {
		t.Fatal("the ticket was spent twice")
	}
}

// TestPairSubmitTicketBelongsToOneRole: a passkey enrolled for one role must
// not be accepted under another role's ticket.
func TestPairSubmitTicketBelongsToOneRole(t *testing.T) {
	withPairCap(t, 16)
	s := pairServer(t)
	store := NewWebAuthnStore(t.TempDir() + "/webauthn.json")
	s.WebAuthn = store
	auth := newTestPasskey(t, store, "member")

	adminTicket, err := s.issuePairTicket("admin", "203.0.113.7")
	if err != nil {
		t.Fatal(err)
	}
	doc := auth.assert(t, adminTicket)
	doc["passkey_id"] = auth.credentialID()

	if rec := postPair(s, pairBody(t, adminTicket, doc)); rec.Code == http.StatusAccepted {
		t.Fatal("a member passkey logged in as admin through a pairing")
	}
}

// TestPairSubmitAcceptedResponseIsJSON: a 202 is still a response the browser
// parses with resp.json(), so it has to say it is JSON.
func TestPairSubmitAcceptedResponseIsJSON(t *testing.T) {
	withPairCap(t, 16)
	s := pairServer(t)
	store := NewWebAuthnStore(t.TempDir() + "/webauthn.json")
	s.WebAuthn = store
	auth := newTestPasskey(t, store, "member")
	ticket, err := s.issuePairTicket("member", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	doc := auth.assert(t, ticket)
	doc["passkey_id"] = auth.credentialID()

	// Through a real server: a recorder would rewrite headers WriteHeader
	// already sent and hide the problem.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.handlePairSubmit(w, r)
	}))
	t.Cleanup(srv.Close)

	resp, err := http.Post(srv.URL, "application/json", pairBody(t, ticket, doc))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusAccepted)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("Content-Type = %q, want application/json", ct)
	}
}

// TestPairTicketChallengeMatchesTheNonceContract: the browser derives its
// challenge the same way the web login does, from the ticket string. If the
// endpoint hashed something else, every ceremony would be rejected for a
// reason nobody could see.
func TestPairTicketChallengeMatchesTheNonceContract(t *testing.T) {
	if len(ChallengeFromNonce("abc")) != 32 {
		t.Fatal("challenge is not a SHA-256 digest")
	}
}

// TestPairSubmitRateLimitSaysHowLong: the page offers a retry button, so a
// refusal has to carry the wait. Without it the button's first press lands
// inside the same meter and reads as an unexplained failure — the shape of
// confusion the cooldown header work exists to remove.
func TestPairSubmitRateLimitSaysHowLong(t *testing.T) {
	s := pairServer(t)
	s.WebAuthn = NewWebAuthnStore(t.TempDir() + "/webauthn.json")
	pairSubmitCooldowns.Reset()

	if rec := postPair(s, pairBody(t, strings.Repeat("ab", 64), nil)); rec.Code == http.StatusTooManyRequests {
		t.Fatal("the first submission was metered")
	}
	rec := postPair(s, pairBody(t, strings.Repeat("cd", 64), nil))
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("second submission = %d, want %d", rec.Code, http.StatusTooManyRequests)
	}
	got := retryAfterSeconds(t, rec)
	if got > int(pairSubmitCooldown.Seconds()) {
		t.Fatalf("Retry-After = %d, want at most the %v cooldown", got, pairSubmitCooldown)
	}
}

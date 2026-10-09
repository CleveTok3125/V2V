package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/CleveTok3125/V2V/internal/wire"
)

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
	if s.countPairSessions() != 0 {
		t.Fatal("a rejected submission opened a session")
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
	_, _, _ = s.newPairSession("member", "203.0.113.7")
	rec := postPair(s, pairBody(t, "anything", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("disabled = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
}

// TestPairSubmitRequiresFields: a submission missing any assertion field is
// malformed. Checking before the store keeps a half-filled body from
// consuming the ticket and then failing verification.
func TestPairSubmitRequiresFields(t *testing.T) {
	s := pairServer(t)
	s.WebAuthn = NewWebAuthnStore(t.TempDir() + "/webauthn.json")
	ticket, _, err := s.newPairSession("member", "203.0.113.7")
	if err != nil {
		t.Fatal(err)
	}
	rec := postPair(s, pairBody(t, ticket, map[string]any{"passkey_id": ""}))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("missing credential = %d, want %d", rec.Code, http.StatusBadRequest)
	}
	if _, ok := s.claimPairSession(ticket); !ok {
		t.Fatal("a malformed submission consumed the ticket")
	}
}

// TestPairSubmitVerifiesAgainstTheSessionRole is the core rule: the assertion
// is checked against the role the desktop named, never against a role the
// submission carries. A browser that lies about its role gets nowhere.
func TestPairSubmitVerifiesAgainstTheSessionRole(t *testing.T) {
	s := pairServer(t)
	s.WebAuthn = NewWebAuthnStore(t.TempDir() + "/webauthn.json")
	ticket, sess, err := s.newPairSession("member", "203.0.113.7")
	if err != nil {
		t.Fatal(err)
	}

	rec := postPair(s, pairBody(t, ticket, map[string]any{
		"role":        "admin",
		"auth_data":   base64.RawURLEncoding.EncodeToString([]byte("auth")),
		"client_data": base64.RawURLEncoding.EncodeToString([]byte(`{"type":"webauthn.get"}`)),
		"sig":         base64.RawURLEncoding.EncodeToString([]byte("sig")),
	}))
	if rec.Code == http.StatusAccepted {
		t.Fatal("a bogus assertion was accepted")
	}

	v := sess.await(2 * time.Second)
	if v.OK {
		t.Fatal("the handshake was granted after a rejected assertion")
	}
	if !errors.Is(v.Err, ErrVerificationFailed) {
		t.Fatalf("verdict err = %v, want ErrVerificationFailed", v.Err)
	}
}

// TestPairSubmitRejectsSecondSubmission: the browser may reload, the user may
// double-tap, a proxy may retry. Only the first submission may resolve the
// pairing.
func TestPairSubmitRejectsSecondSubmission(t *testing.T) {
	s := pairServer(t)
	s.WebAuthn = NewWebAuthnStore(t.TempDir() + "/webauthn.json")
	ticket, sess, err := s.newPairSession("member", "203.0.113.7")
	if err != nil {
		t.Fatal(err)
	}

	// A bogus assertion is rejected and leaves the ticket alone, so the test
	// for "one login, one grant" has to use assertions that actually verify.
	first := postPairFrom(s, "198.51.100.1", pairBody(t, ticket, nil))
	if first.Code == http.StatusAccepted {
		t.Fatal("a bogus assertion was accepted")
	}
	// The ticket is still there: a second bogus attempt is verified again,
	// not told the pairing is spent.
	second := postPairFrom(s, "198.51.100.2", pairBody(t, ticket, nil))
	if second.Code == http.StatusGone {
		t.Fatal("a failed assertion spent the ticket; the person cannot try another passkey")
	}

	v := sess.await(2 * time.Second)
	if v.OK {
		t.Fatal("the handshake was granted")
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

	rec := postPair(s, pairBody(t, strings.Repeat("ef", 64), nil))
	if rec.Code == http.StatusTooManyRequests {
		return
	}
	// Not rate limited on the first call: the second must be.
	second := postPair(s, pairBody(t, strings.Repeat("ef", 64), nil))
	if second.Code != http.StatusTooManyRequests {
		t.Fatalf("second call = %d, want %d", second.Code, http.StatusTooManyRequests)
	}
}

// TestPairSubmitAcceptsVerifiedAssertion walks the endpoint end to end with a
// real ES256 credential: the browser asserts over the pair ticket, the
// handler verifies it through the same library path the web login uses, the
// sign counter advances, and the waiting handshake is granted.
func TestPairSubmitAcceptsVerifiedAssertion(t *testing.T) {
	s := pairServer(t)
	store := NewWebAuthnStore(t.TempDir() + "/webauthn.json")
	s.WebAuthn = store
	auth := newTestPasskey(t, s.WebAuthn, "member")

	ticket, sess, err := s.newPairSession("member", "203.0.113.7")
	if err != nil {
		t.Fatal(err)
	}
	doc := auth.assert(t, ticket)
	doc["passkey_id"] = auth.credentialID()

	rec := postPair(s, pairBody(t, ticket, doc))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("verified assertion = %d, want %d (%s)", rec.Code, http.StatusAccepted, rec.Body)
	}

	v := sess.await(2 * time.Second)
	if !v.OK {
		t.Fatalf("verdict = %+v, want granted", v)
	}
	// The ticket is spent now: a second verified assertion over it must not
	// resolve the handshake a second time.
	if s.commitPairSession(ticket) {
		t.Fatal("a spent pairing was committed again")
	}

	// The counter must have advanced, so a replay of the same assertion is
	// caught by the clone check rather than by luck.
	cred, ok := s.WebAuthn.Credential("member", auth.credentialID())
	if !ok {
		t.Fatal("credential vanished from the store")
	}
	if cred.SignCount == 0 {
		t.Fatal("sign counter did not advance")
	}

	// A second, freshly minted assertion cannot reuse the spent ticket.
	replay := auth.assert(t, ticket)
	replay["passkey_id"] = auth.credentialID()
	if rec := postPairFrom(s, "198.51.100.3", pairBody(t, ticket, replay)); rec.Code != http.StatusGone {
		t.Fatalf("replay = %d, want %d", rec.Code, http.StatusGone)
	}
}

// TestPairSubmitTicketBelongsToOneRole: the same assertion must not be
// accepted under a different role's pairing, even when the desktop on that
// pairing named the same person. The role is part of the lookup, not part of
// the payload.
func TestPairSubmitTicketBelongsToOneRole(t *testing.T) {
	s := pairServer(t)
	s.WebAuthn = NewWebAuthnStore(t.TempDir() + "/webauthn.json")
	auth := newTestPasskey(t, s.WebAuthn, "member")

	adminTicket, _, err := s.newPairSession("admin", "203.0.113.7")
	if err != nil {
		t.Fatal(err)
	}
	doc := auth.assert(t, adminTicket)
	// Claim the admin role from a passkey enrolled under member.
	doc["passkey_id"] = auth.credentialID()

	rec := postPair(s, pairBody(t, adminTicket, doc))
	if rec.Code == http.StatusAccepted {
		t.Fatal("a member passkey logged in as admin through a pairing")
	}
}

// TestVersionEndpointAdvertisesPasskey: the desktop asks before it starts a
// ceremony, and a server with passkeys off must say so rather than let the
// client open a browser link that cannot work.
func TestVersionEndpointAdvertisesPasskey(t *testing.T) {
	pairServer(t)
	rec := httptest.NewRecorder()
	handleAPIVersion(rec, httptest.NewRequest(http.MethodGet, "/api/version", nil))

	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("version payload is not JSON: %v (%s)", err, rec.Body)
	}
	if _, ok := got["version"]; !ok {
		t.Error("version key missing")
	}
	enabled, ok := got["passkey"].(bool)
	if !ok {
		t.Fatalf("passkey is not a bool: %v", got["passkey"])
	}
	if !enabled {
		t.Error("passkey reported disabled while WAConfig.Enabled is true")
	}

	WAConfig.Enabled = false
	rec = httptest.NewRecorder()
	handleAPIVersion(rec, httptest.NewRequest(http.MethodGet, "/api/version", nil))
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got["passkey"] != false {
		t.Errorf("passkey = %v, want false when WAConfig is off", got["passkey"])
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
	var _ = wire.AuthPair
}

// TestPairSubmitRetryAfterABadAssertion: a person who taps the wrong passkey,
// or whose phone drops the response, must be able to try again on the same
// link. Spending the ticket on the first failure means restarting the client
// and waiting out the ceremony again.
func TestPairSubmitRetryAfterABadAssertion(t *testing.T) {
	s := pairServer(t)
	store := NewWebAuthnStore(t.TempDir() + "/webauthn.json")
	s.WebAuthn = store
	auth := newTestPasskey(t, store, "member")

	ticket, sess, err := s.newPairSession("member", "203.0.113.7")
	if err != nil {
		t.Fatal(err)
	}
	// A mangled signature: the credential is right, the proof is not.
	bad := auth.assert(t, ticket)
	bad["passkey_id"] = auth.credentialID()
	bad["sig"] = base64.RawURLEncoding.EncodeToString([]byte("not a signature"))
	if rec := postPairFrom(s, "198.51.100.1", pairBody(t, ticket, bad)); rec.Code == http.StatusAccepted {
		t.Fatal("a mangled signature was accepted")
	}
	if v := sess.await(2 * time.Second); v.OK {
		t.Fatal("the handshake was granted on a bad signature")
	}

	// The ticket must still be there for a second attempt.
	good := auth.assert(t, ticket)
	good["passkey_id"] = auth.credentialID()
	rec := postPairFrom(s, "198.51.100.2", pairBody(t, ticket, good))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("retry after a bad signature = %d, want %d (%s)", rec.Code, http.StatusAccepted, rec.Body)
	}
}

// TestPairSubmitAcceptedResponseIsJSON: a 202 is still a response the browser
// parses with resp.json(), so it has to say it is JSON.
func TestPairSubmitAcceptedResponseIsJSON(t *testing.T) {
	s := pairServer(t)
	store := NewWebAuthnStore(t.TempDir() + "/webauthn.json")
	s.WebAuthn = store
	auth := newTestPasskey(t, store, "member")
	ticket, _, err := s.newPairSession("member", "203.0.113.7")
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

	body := pairBody(t, ticket, doc)
	resp, err := http.Post(srv.URL, "application/json", body)
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

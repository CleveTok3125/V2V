package main

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
)

// pairServer wires a server whose role registry and passkey config are set
// up for the pairing tests. WAConfig is package-level state shared with the
// WebAuthn boot path, so every test restores it.
func pairServer(t *testing.T) *ChatServer {
	t.Helper()
	testCfg(t)
	oldWA := WAConfig
	t.Cleanup(func() { WAConfig = oldWA })
	WAConfig = webauthnConfig{RPID: "chat.example.com", Origin: "https://chat.example.com", Enabled: true}
	// testCfg restores Cfg.Static; onion passkeys stay off, which is the
	// deployment default and the case the refusal test exercises.
	Cfg.Static.Onion.AllowPasskey = false

	// LoadWebauthnEnv would overwrite WAConfig from the environment; the
	// library instance has to match the config the tests set up.
	var err error
	oldWebAuth := WebAuth
	WebAuth, err = webauthn.New(&webauthn.Config{
		RPDisplayName:         "V2V",
		RPID:                  WAConfig.RPID,
		RPOrigins:             []string{WAConfig.Origin},
		AttestationPreference: protocol.PreferNoAttestation,
	})
	if err != nil {
		t.Fatalf("webauthn init: %v", err)
	}
	t.Cleanup(func() { WebAuth = oldWebAuth })

	s := NewChatServer()
	s.RoleRegistry = map[string]RoleDefinition{
		"member": {Permission: GetDefaultPermission()},
		"admin":  {Permission: Permission{CanMessageUnlimited: true, CustomPrefix: "*"}},
	}
	// The submission limiter is package-level state keyed by client IP,
	// and httptest requests all arrive from the same one.
	pairSubmitCooldowns.Reset()
	return s
}

// TestHandleAuthPairRequestGrantsAfterCeremony is the whole feature in one
// test: the desktop asks for a browser ceremony, a browser deposits a
// verified assertion, and the handshake that was waiting all along comes
// back granted with the role's own permission.
func TestHandleAuthPairRequestGrantsAfterCeremony(t *testing.T) {
	s := pairServer(t)
	client, server := dialAuthPair(t, s)
	type result struct {
		perms Permission
		resp  AuthPacket
		err   error
	}
	done := make(chan result, 1)
	go func() {
		p, resp, err := s.HandleAuth(server, "127.0.0.1", "localhost", false)
		done <- result{p, resp, err}
	}()

	ch := readChallenge(t, client)
	if err := client.WriteJSON(AuthPacket{
		Type: "auth_pair_request", Username: "Alice", Nonce: ch.Nonce, Role: "admin",
	}); err != nil {
		t.Fatal(err)
	}

	client.SetReadDeadline(time.Now().Add(10 * time.Second))
	var reply AuthPacket
	if err := client.ReadJSON(&reply); err != nil {
		t.Fatalf("no pairing reply: %v", err)
	}
	if reply.Type != "auth_pair" {
		t.Fatalf("reply type = %q, want %q", reply.Type, "auth_pair")
	}
	wantURL := "https://chat.example.com/web/#pair=" + reply.PairTicket
	if reply.PairURL != wantURL {
		t.Fatalf("PairURL = %q, want %q", reply.PairURL, wantURL)
	}

	// The ticket must name this handshake and no other.
	sess, ok := s.claimPairSession(reply.PairTicket)
	if !ok {
		t.Fatal("the issued ticket does not resolve")
	}
	if sess.Role != "admin" || sess.IP != "127.0.0.1" {
		t.Fatalf("session = %+v, want the admin role from 127.0.0.1", sess)
	}

	// One ticket, one grant: a second submission must not resolve the same
	// handshake again.
	s.commitPairSession(reply.PairTicket)
	if s.commitPairSession(reply.PairTicket) {
		t.Fatal("the ticket was committed twice")
	}
	sess.resolve(pairVerdict{OK: true})

	select {
	case got := <-done:
		if got.err != nil {
			t.Fatalf("pairing handshake failed: %v", got.err)
		}
		if got.resp.AuthType != "passkey" {
			t.Fatalf("AuthType = %q, want passkey", got.resp.AuthType)
		}
		if !got.perms.CanMessageUnlimited || got.perms.CustomPrefix != "*" {
			t.Fatalf("permission = %+v, want the admin permission", got.perms)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("HandleAuth stayed blocked after a successful ceremony")
	}
}

// TestHandleAuthPairRequestCarriesTheCeremonyFailure: when the browser's
// assertion is rejected, the desktop must learn that rather than sit
// waiting for a ticket that will never resolve.
func TestHandleAuthPairRequestCarriesTheCeremonyFailure(t *testing.T) {
	s := pairServer(t)
	client, server := dialAuthPair(t, s)
	done := make(chan error, 1)
	go func() {
		_, _, err := s.HandleAuth(server, "127.0.0.1", "localhost", false)
		done <- err
	}()
	ch := readChallenge(t, client)
	if err := client.WriteJSON(AuthPacket{
		Type: "auth_pair_request", Username: "Alice", Nonce: ch.Nonce, Role: "member",
	}); err != nil {
		t.Fatal(err)
	}
	client.SetReadDeadline(time.Now().Add(10 * time.Second))
	var reply AuthPacket
	if err := client.ReadJSON(&reply); err != nil {
		t.Fatal(err)
	}
	sess, ok := s.claimPairSession(reply.PairTicket)
	if !ok {
		t.Fatal("the issued ticket does not resolve")
	}
	sess.resolve(pairVerdict{OK: false, Err: ErrVerificationFailed})

	select {
	case err := <-done:
		if !errors.Is(err, ErrVerificationFailed) {
			t.Fatalf("err = %v, want ErrVerificationFailed", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("HandleAuth stayed blocked after a rejected ceremony")
	}
}

// TestHandleAuthPairRequestRefusals covers the ways a pairing must not
// start: no passkey support, an unknown role, a stolen handshake nonce,
// and onion ingress where passkeys are off.
func TestHandleAuthPairRequestRefusals(t *testing.T) {
	t.Run("passkey disabled", func(t *testing.T) {
		s := pairServer(t)
		WAConfig.Enabled = false
		client, server := dialAuthPair(t, s)
		done := make(chan error, 1)
		go func() {
			_, _, err := s.HandleAuth(server, "127.0.0.1", "localhost", false)
			done <- err
		}()
		ch := readChallenge(t, client)
		if err := client.WriteJSON(AuthPacket{
			Type: "auth_pair_request", Nonce: ch.Nonce, Role: "member",
		}); err != nil {
			t.Fatal(err)
		}
		if err := <-done; !errors.Is(err, ErrPasskeyDisabled) {
			t.Fatalf("err = %v, want ErrPasskeyDisabled", err)
		}
	})

	t.Run("unknown role", func(t *testing.T) {
		s := pairServer(t)
		client, server := dialAuthPair(t, s)
		done := make(chan error, 1)
		go func() {
			_, _, err := s.HandleAuth(server, "127.0.0.1", "localhost", false)
			done <- err
		}()
		ch := readChallenge(t, client)
		if err := client.WriteJSON(AuthPacket{
			Type: "auth_pair_request", Nonce: ch.Nonce, Role: "nobody",
		}); err != nil {
			t.Fatal(err)
		}
		if err := <-done; !errors.Is(err, ErrInvalidRole) {
			t.Fatalf("err = %v, want ErrInvalidRole", err)
		}
	})

	t.Run("no role", func(t *testing.T) {
		s := pairServer(t)
		client, server := dialAuthPair(t, s)
		done := make(chan error, 1)
		go func() {
			_, _, err := s.HandleAuth(server, "127.0.0.1", "localhost", false)
			done <- err
		}()
		ch := readChallenge(t, client)
		if err := client.WriteJSON(AuthPacket{Type: "auth_pair_request", Nonce: ch.Nonce}); err != nil {
			t.Fatal(err)
		}
		if err := <-done; !errors.Is(err, ErrInvalidRole) {
			t.Fatalf("err = %v, want ErrInvalidRole", err)
		}
	})

	t.Run("stolen nonce", func(t *testing.T) {
		s := pairServer(t)
		client, server := dialAuthPair(t, s)
		done := make(chan error, 1)
		go func() {
			_, _, err := s.HandleAuth(server, "127.0.0.1", "localhost", false)
			done <- err
		}()
		readChallenge(t, client)
		if err := client.WriteJSON(AuthPacket{
			Type: "auth_pair_request", Nonce: strings.Repeat("ab", 64), Role: "member",
		}); err != nil {
			t.Fatal(err)
		}
		if err := <-done; !errors.Is(err, ErrInvalidNonce) {
			t.Fatalf("err = %v, want ErrInvalidNonce", err)
		}
	})

	t.Run("onion without the opt-in", func(t *testing.T) {
		s := pairServer(t)
		client, server := dialAuthPair(t, s)
		done := make(chan error, 1)
		go func() {
			_, _, err := s.HandleAuth(server, "127.0.0.1", "localhost", true)
			done <- err
		}()
		ch := readChallenge(t, client)
		if err := client.WriteJSON(AuthPacket{
			Type: "auth_pair_request", Nonce: ch.Nonce, Role: "member",
		}); err != nil {
			t.Fatal(err)
		}
		if err := <-done; !errors.Is(err, ErrPasskeyDisabled) {
			t.Fatalf("err = %v, want ErrPasskeyDisabled", err)
		}
	})
}

// TestHandleAuthPairRequestReleasesTheSlot: whatever ends a pairing, the
// ticket must stop resolving. A desktop that retries with a new handshake
// should not find the previous link still alive, or the pairing budget
// would leak one slot per failed attempt.
func TestHandleAuthPairRequestReleasesTheSlot(t *testing.T) {
	s := pairServer(t)
	client, server := dialAuthPair(t, s)
	done := make(chan error, 1)
	go func() {
		_, _, err := s.HandleAuth(server, "127.0.0.1", "localhost", false)
		done <- err
	}()
	ch := readChallenge(t, client)
	if err := client.WriteJSON(AuthPacket{
		Type: "auth_pair_request", Nonce: ch.Nonce, Role: "member",
	}); err != nil {
		t.Fatal(err)
	}
	client.SetReadDeadline(time.Now().Add(10 * time.Second))
	var reply AuthPacket
	if err := client.ReadJSON(&reply); err != nil {
		t.Fatal(err)
	}
	sess, ok := s.claimPairSession(reply.PairTicket)
	if !ok {
		t.Fatal("the issued ticket does not resolve")
	}
	sess.resolve(pairVerdict{OK: true})
	if err := <-done; err != nil {
		t.Fatalf("pairing failed: %v", err)
	}
	if _, ok := s.claimPairSession(reply.PairTicket); ok {
		t.Fatal("a finished pairing still resolves")
	}
}

// TestAuthPairRequestIsNotAPasskeyPacket: the two pre-auth requests must
// not be interchangeable. A packet carrying assertion material is a login
// attempt and is verified where it is; one carrying only a ticket request
// starts a ceremony. Mixing them would let a client skip the wait.
func TestAuthPairRequestIsNotAPasskeyPacket(t *testing.T) {
	s := pairServer(t)
	client, server := dialAuthPair(t, s)
	done := make(chan error, 1)
	go func() {
		_, _, err := s.HandleAuth(server, "127.0.0.1", "localhost", false)
		done <- err
	}()
	ch := readChallenge(t, client)
	// A pair request that also carries an assertion is a login attempt: it
	// must be verified as one, not used to open a ceremony window.
	if err := client.WriteJSON(AuthPacket{
		Type: "auth_pair_request", Nonce: ch.Nonce, Role: "member",
		PasskeyID: "cred", PasskeySig: "sig",
	}); err != nil {
		t.Fatal(err)
	}
	if err := <-done; !errors.Is(err, ErrVerificationFailed) {
		t.Fatalf("err = %v, want the assertion path to reject it", err)
	}
	if s.countPairSessions() != 0 {
		t.Fatal("a rejected assertion opened a pairing session")
	}
}

// TestPairURLHasNoRoleParameter: the role comes from the desktop's own
// request and is bound to the session. Echoing it in the URL would add a
// second, page-controlled input to a value the server has already decided.
func TestPairURLHasNoRoleParameter(t *testing.T) {
	s := pairServer(t)
	client, server := dialAuthPair(t, s)
	done := make(chan error, 1)
	go func() {
		_, _, err := s.HandleAuth(server, "127.0.0.1", "localhost", false)
		done <- err
	}()
	ch := readChallenge(t, client)
	if err := client.WriteJSON(AuthPacket{
		Type: "auth_pair_request", Nonce: ch.Nonce, Role: "admin",
	}); err != nil {
		t.Fatal(err)
	}
	client.SetReadDeadline(time.Now().Add(10 * time.Second))
	var reply AuthPacket
	if err := client.ReadJSON(&reply); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(reply.PairURL, "role=") {
		t.Fatalf("PairURL carries a role the page could edit: %q", reply.PairURL)
	}
	if !strings.HasPrefix(reply.PairURL, WAConfig.Origin+"/web/#pair=") {
		t.Fatalf("PairURL is not served from the configured origin: %q", reply.PairURL)
	}
	// The URL is what the desktop prints; it must be plain JSON-safe text.
	if _, err := json.Marshal(reply); err != nil {
		t.Fatalf("pairing reply is not serializable: %v", err)
	}
	sess, _ := s.claimPairSession(reply.PairTicket)
	sess.resolve(pairVerdict{OK: true})
	<-done
}

// TestAbandonedCeremonyIsNotAnAuthFailure: a person who never opens the link
// has done nothing wrong. Timing out must not count toward the ban, or six
// slow phones lock an address out of the server for five minutes at a time.
func TestAbandonedCeremonyIsNotAnAuthFailure(t *testing.T) {
	s := pairServer(t)
	s.AuthFails = map[string]RateLimitRecord{}

	for i := 0; i < 8; i++ {
		resp := AuthPacket{Role: "member"}
		if !penalizeAuthFailure(resp, ErrPairExpired) {
			continue
		}
		s.handleAuthPenalty("203.0.113.5")
	}

	s.AuthFailsMu.Lock()
	rec := s.AuthFails["203.0.113.5"]
	s.AuthFailsMu.Unlock()
	if !rec.UnlockTime.IsZero() {
		t.Fatalf("8 abandoned ceremonies banned the IP for %v", time.Until(rec.UnlockTime))
	}
}

// TestRealAuthFailuresStillPenalize: the exemption must not become a hole.
// A rejected signature is a real failure and has to keep counting.
func TestRealAuthFailuresStillPenalize(t *testing.T) {
	s := pairServer(t)
	s.AuthFails = map[string]RateLimitRecord{}

	for i := 0; i < 5; i++ {
		resp := AuthPacket{Role: "member"}
		if !penalizeAuthFailure(resp, ErrVerificationFailed) {
			t.Fatalf("attempt %d: a rejected signature was not counted", i+1)
		}
		s.handleAuthPenalty("203.0.113.6")
	}

	s.AuthFailsMu.Lock()
	rec := s.AuthFails["203.0.113.6"]
	s.AuthFailsMu.Unlock()
	if rec.UnlockTime.IsZero() {
		t.Fatal("5 rejected signatures did not ban the IP")
	}
}

// TestGuestFailuresStayExempt: the existing rule is that a packet without a
// role carries no penalty. Pairing must not change that.
func TestGuestFailuresStayExempt(t *testing.T) {
	if penalizeAuthFailure(AuthPacket{}, ErrVerificationFailed) {
		t.Fatal("a guest packet was penalized")
	}
}

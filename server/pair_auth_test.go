package main

import (
	"encoding/json"
	"errors"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/CleveTok3125/V2V/internal/wire"
	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
)

// pairServer wires a server whose role registry and passkey config suit the
// pairing tests. WAConfig and WebAuth are package-level state shared with
// the boot path, so every test restores them.
func pairServer(t *testing.T) *ChatServer {
	t.Helper()
	testCfg(t)
	oldWA := WAConfig
	oldWebAuth := WebAuth
	t.Cleanup(func() {
		WAConfig = oldWA
		WebAuth = oldWebAuth
	})
	WAConfig = webauthnConfig{RPID: "chat.example.com", Origin: "https://chat.example.com", Enabled: true}
	// testCfg restores Cfg.Static; onion passkeys stay off, which is the
	// deployment default and the case the refusal test exercises.
	Cfg.Static.Onion.AllowPasskey = false

	var err error
	WebAuth, err = webauthn.New(&webauthn.Config{
		RPDisplayName:         "V2V",
		RPID:                  WAConfig.RPID,
		RPOrigins:             []string{WAConfig.Origin},
		AttestationPreference: protocol.PreferNoAttestation,
	})
	if err != nil {
		t.Fatalf("webauthn init: %v", err)
	}

	s := NewChatServer()
	s.RoleRegistry = map[string]RoleDefinition{
		"member": {Permission: GetDefaultPermission()},
		"admin":  {Permission: Permission{CanMessageUnlimited: true, CustomPrefix: "*"}},
	}
	// The poll and submission meters are package-level and keyed by client
	// IP, and httptest requests all arrive from the same one.
	pairSubmitCooldowns.Reset()
	pairStatusCooldowns.Reset()
	return s
}

// TestPairRequestIssuesAndCloses is the shape of the whole feature: the
// desktop asks, gets a ticket and a link, and this socket ends. Nothing is
// left waiting here, because the wait belongs to poll requests.
func TestPairRequestIssuesAndCloses(t *testing.T) {
	s := pairServer(t)
	client, server := dialAuthPair(t, s)
	done := make(chan error, 1)
	go func() {
		_, _, err := s.HandleAuth(server, "127.0.0.1", "localhost", false)
		done <- err
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
		t.Fatalf("reply type = %q, want auth_pair", reply.Type)
	}
	if want := WAConfig.Origin + "/web/#pair=" + reply.PairTicket; reply.PairURL != want {
		t.Fatalf("PairURL = %q, want %q", reply.PairURL, want)
	}
	if reply.PairTTLSec != int(pairTicketTTL.Seconds()) {
		t.Fatalf("PairTTLSec = %d, want %d", reply.PairTTLSec, int(pairTicketTTL.Seconds()))
	}

	select {
	case err := <-done:
		if !errors.Is(err, errPairTicketIssued) {
			t.Fatalf("err = %v, want errPairTicketIssued", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("HandleAuth kept waiting instead of ending the socket")
	}

	ticket, ok := s.pairTicketFor(reply.PairTicket, "127.0.0.1")
	if !ok {
		t.Fatal("the issued ticket does not resolve")
	}
	if ticket.Role != "admin" {
		t.Fatalf("ticket role = %q, want the requested admin", ticket.Role)
	}
}

// TestPairRequestRefusals covers the ways a pairing must not start: no
// passkey support, onion ingress, an unknown role, a missing role and a
// stolen handshake nonce. None of them may mint a ticket.
func TestPairRequestRefusals(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*ChatServer)
		onion   bool
		nonce   string
		role    string
		wantErr error
	}{
		{name: "passkey disabled", mutate: func(*ChatServer) { WAConfig.Enabled = false }, wantErr: ErrPasskeyDisabled},
		{name: "onion without the opt-in", onion: true, wantErr: ErrPasskeyDisabled},
		{name: "unknown role", role: "nobody", wantErr: ErrInvalidRole},
		{name: "no role", wantErr: ErrInvalidRole},
		{name: "stolen nonce", nonce: strings.Repeat("ab", 64), role: "member", wantErr: ErrInvalidNonce},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := pairServer(t)
			if c.mutate != nil {
				c.mutate(s)
			}
			client, server := dialAuthPair(t, s)
			done := make(chan error, 1)
			go func() {
				_, _, err := s.HandleAuth(server, "127.0.0.1", "localhost", c.onion)
				done <- err
			}()
			ch := readChallenge(t, client)
			nonce := c.nonce
			if nonce == "" {
				nonce = ch.Nonce
			}
			if err := client.WriteJSON(AuthPacket{
				Type: "auth_pair_request", Nonce: nonce, Role: c.role,
			}); err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-done:
				if !errors.Is(err, c.wantErr) {
					t.Fatalf("err = %v, want %v", err, c.wantErr)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("HandleAuth did not return")
			}
			if s.countPairTickets() != 0 {
				t.Fatal("a refused request minted a ticket")
			}
		})
	}
}

// TestPairRequestWithAssertionIsALoginAttempt: a packet carrying assertion
// material meant to authenticate here, so it must be verified here rather
// than used to open a ceremony.
func TestPairRequestWithAssertionIsALoginAttempt(t *testing.T) {
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
		PasskeyID: "cred", PasskeySig: "sig",
	}); err != nil {
		t.Fatal(err)
	}
	if err := <-done; !errors.Is(err, ErrVerificationFailed) {
		t.Fatalf("err = %v, want the assertion path to reject it", err)
	}
	if s.countPairTickets() != 0 {
		t.Fatal("a rejected assertion minted a ticket")
	}
}

// TestTicketLoginGrantsTheTicketRole: the role comes from the ticket, not
// from the packet, so a client cannot claim a role its ceremony never
// proved.
func TestTicketLoginGrantsTheTicketRole(t *testing.T) {
	withPairCap(t, 16)
	s := pairServer(t)
	client, server := dialAuthPair(t, s)
	done := make(chan struct {
		perms Permission
		resp  AuthPacket
		err   error
	}, 1)
	go func() {
		p, resp, err := s.HandleAuth(server, "127.0.0.1", "localhost", false)
		done <- struct {
			perms Permission
			resp  AuthPacket
			err   error
		}{p, resp, err}
	}()

	ticket, err := s.issuePairTicket("admin", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	if !s.markPairVerified(ticket) {
		t.Fatal("markPairVerified refused a live ticket")
	}

	ch := readChallenge(t, client)
	if err := client.WriteJSON(AuthPacket{
		Nonce: ch.Nonce,
		// A role the packet claims but the ticket never proved.
		Role:       "member",
		PairTicket: ticket,
	}); err != nil {
		t.Fatal(err)
	}
	got := <-done
	if got.err != nil {
		t.Fatalf("ticket login failed: %v", got.err)
	}
	if got.resp.Role != "admin" || got.resp.AuthType != "passkey" {
		t.Fatalf("resp = %+v, want the admin role from the ticket", got.resp)
	}
	if !got.perms.CanMessageUnlimited || got.perms.CustomPrefix != "*" {
		t.Fatalf("permission = %+v, want the admin permission", got.perms)
	}
}

// TestTicketLoginRefusals: an unproved, spent, unknown or foreign-address
// ticket must all be refused.
func TestTicketLoginRefusals(t *testing.T) {
	withPairCap(t, 16)

	t.Run("unproved", func(t *testing.T) {
		s := pairServer(t)
		ticket, err := s.issuePairTicket("member", "127.0.0.1")
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := s.loginWithPairTicket("127.0.0.1", AuthPacket{PairTicket: ticket}); !errors.Is(err, ErrPairUnknown) {
			t.Fatalf("err = %v, want ErrPairUnknown", err)
		}
	})

	t.Run("spent", func(t *testing.T) {
		s := pairServer(t)
		ticket, err := s.issuePairTicket("member", "127.0.0.1")
		if err != nil {
			t.Fatal(err)
		}
		s.markPairVerified(ticket)
		if _, _, err := s.loginWithPairTicket("127.0.0.1", AuthPacket{PairTicket: ticket}); err != nil {
			t.Fatalf("first spend refused: %v", err)
		}
		if _, _, err := s.loginWithPairTicket("127.0.0.1", AuthPacket{PairTicket: ticket}); !errors.Is(err, ErrPairUnknown) {
			t.Fatalf("second spend err = %v, want ErrPairUnknown", err)
		}
	})

	t.Run("unknown", func(t *testing.T) {
		s := pairServer(t)
		if _, _, err := s.loginWithPairTicket("127.0.0.1", AuthPacket{PairTicket: strings.Repeat("cd", 64)}); !errors.Is(err, ErrPairUnknown) {
			t.Fatalf("err = %v, want ErrPairUnknown", err)
		}
	})

	t.Run("another address", func(t *testing.T) {
		s := pairServer(t)
		ticket, err := s.issuePairTicket("member", "198.51.100.9")
		if err != nil {
			t.Fatal(err)
		}
		s.markPairVerified(ticket)
		if _, _, err := s.loginWithPairTicket("127.0.0.1", AuthPacket{PairTicket: ticket}); !errors.Is(err, ErrPairUnknown) {
			t.Fatalf("err = %v, want ErrPairUnknown", err)
		}
	})
}

// TestAuthErrIsProbe pins the classification. The axis is direction: an
// event that returns resources is the outcome the server wants, so it must
// never be recorded, while a client that keeps asking past a budget or
// asks with proof that does not verify is spending the server's attention.
func TestAuthErrIsProbe(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nothing happened", nil, false},
		{"pairing moved off the socket", errPairTicketIssued, false},
		{"ran out of time", ErrPairExpired, false},
		{"server out of entropy", ErrEntropyExhaustion, false},
		{"server turned passkeys off", ErrPasskeyDisabled, false},
		{"client vanished mid-read", io.EOF, false},
		{"client connection reset", net.ErrClosed, false},
		{"bad signature", ErrVerificationFailed, true},
		{"unknown ticket", ErrPairUnknown, true},
		{"memory ceiling reached", ErrPairCapacity, true},
		{"replayed nonce", ErrInvalidNonce, true},
		{"expired nonce", ErrExpiredNonce, true},
		{"stolen nonce", ErrIPMismatch, true},
		{"oversized payload", ErrPayloadTooLarge, true},
		{"role too long", ErrInvalidRoleLength, true},
		{"unknown role", ErrInvalidRole, true},
		{"tripcode too long", ErrTripcodeTooLong, true},
	}
	for _, c := range cases {
		if got := authErrIsProbe(c.err); got != c.want {
			t.Errorf("authErrIsProbe(%v) = %v, want %v (%s)", c.err, got, c.want, c.name)
		}
	}
}

// TestPenalizeSharesTheClassification: the ban must read the same rule as
// the behaviour ring, so the two cannot drift apart as they did when each
// carried its own list.
func TestPenalizeSharesTheClassification(t *testing.T) {
	member := AuthPacket{Role: "member"}
	if penalizeAuthFailure(AuthPacket{}, ErrVerificationFailed) {
		t.Error("a packet with no role was penalized")
	}
	if penalizeAuthFailure(member, ErrPairExpired) {
		t.Error("an expired pairing was penalized")
	}
	if !penalizeAuthFailure(member, ErrVerificationFailed) {
		t.Error("a rejected signature was not penalized")
	}
}

// TestTicketLoginBecomesAWorkingSession closes the loop the ticket flow
// exists for: a browser proved a ticket, and the connection that presents
// it must end up as an ordinary session — authenticated, registered, and
// receiving the same opening frames every other login gets. Asserting only
// the permission would leave "and then the connection works" unproven.
func TestTicketLoginBecomesAWorkingSession(t *testing.T) {
	s := pairServer(t)
	s.Chain.appendMessageToHistory(tagLine(1, "chat", "", "line"))

	client, server := dialAuthPair(t, s)
	served := make(chan struct{})
	go func() {
		defer close(served)
		session, err := s.authenticateClient(server, "127.0.0.1", "localhost", false)
		if err != nil {
			server.Close()
			return
		}
		s.serveAuthenticated(session, "127.0.0.1")
	}()

	ticket, err := s.issuePairTicket("admin", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	if !s.markPairVerified(ticket) {
		t.Fatal("markPairVerified refused a live ticket")
	}

	ch := readChallenge(t, client)
	if err := client.WriteJSON(AuthPacket{Nonce: ch.Nonce, PairTicket: ticket}); err != nil {
		t.Fatal(err)
	}

	// auth_success, then the opening frames a registered client receives.
	var sawSuccess, sawInfo, sawJoin bool
	deadline := time.Now().Add(15 * time.Second)
	for !(sawSuccess && sawInfo && sawJoin) {
		client.SetReadDeadline(deadline)
		_, msg, err := client.ReadMessage()
		if err != nil {
			t.Fatalf("read failed before the session opened: %v", err)
		}
		var packet AuthPacket
		if json.Unmarshal(msg, &packet) == nil && packet.Type == wire.AuthSuccess {
			sawSuccess = true
			if packet.AuthType != "passkey" || packet.Role != "admin" {
				t.Fatalf("auth_success = %+v, want a passkey admin session", packet)
			}
		}
		var info HistoryInfo
		if json.Unmarshal(msg, &info) == nil && info.Type == "history_info" {
			sawInfo = true
		}
		if strings.Contains(string(msg), "đã tham gia phòng chat") {
			sawJoin = true
		}
	}
	client.Close()
	// The session goroutine reads server config, so it has to be joined
	// before the test returns and its cleanup restores Cfg.
	select {
	case <-served:
	case <-time.After(10 * time.Second):
		t.Fatal("serveAuthenticated did not return after client close")
	}
}

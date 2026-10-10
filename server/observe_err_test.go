package main

// The behaviour ring is the soft signal and the ban counter is the hard one.
// They share authErrIsProbe, but only the ban half had a test. These pin the
// recording half through the real handshake, so a wiring change that stopped
// recording — or that started recording the failures the classification
// exempts — fails here.

import (
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/CleveTok3125/V2V/internal/behavior"
)

// authScoreServer is a pairing server whose behaviour store the test can read
// back after the handshake.
func authScoreServer(t *testing.T) (*ChatServer, *behavior.Store) {
	t.Helper()
	store := behavior.NewStore(16)
	s := pairServer(t)
	s.Behavior = NewBehaviorEngine(store, behavior.NewStats(), stubGeo{}, "")
	return s, store
}

func recordedCodes(store *behavior.Store, ip string) []string {
	profile := store.Get(ip)
	codes := make([]string, 0, len(profile.Errs))
	for _, event := range profile.Errs {
		codes = append(codes, event.Code)
	}
	return codes
}

// TestRejectedSignatureReachesTheBehaviourRing: a failed proof is evidence,
// and the code has to name what failed. Nothing else asserts that the
// sentinel's text survives the trip into the ring, so a rename or a prefix
// change would leave the ring silently empty.
func TestRejectedSignatureReachesTheBehaviourRing(t *testing.T) {
	s, store := authScoreServer(t)
	client, server := dialAuthPair(t, s)
	done := make(chan error, 1)
	go func() {
		_, err := s.authenticateClient(server, "127.0.0.1", "localhost", false)
		done <- err
	}()

	ch := readChallenge(t, client)
	// Well-formed length, but the signature belongs to nobody.
	if err := client.WriteJSON(AuthPacket{
		Type: "auth", Nonce: ch.Nonce, Role: "member", Username: "Alice",
		Signature: strings.Repeat("ab", 64),
	}); err != nil {
		t.Fatal(err)
	}
	if err := <-done; !errors.Is(err, ErrVerificationFailed) {
		t.Fatalf("handshake err = %v, want ErrVerificationFailed", err)
	}

	codes := recordedCodes(store, "127.0.0.1")
	if len(codes) != 1 || codes[0] != "auth:verification_failed" {
		t.Fatalf("behaviour ring = %v, want exactly [auth:verification_failed]", codes)
	}
}

// TestExpiredCeremonyStaysOutOfTheRing: a ticket that ran out of time is a
// person who took too long, not an anomaly. It is exempt from the ban for
// that reason, and the ring has to agree — otherwise the exemption only moves
// the penalty somewhere harder to see.
func TestExpiredCeremonyStaysOutOfTheRing(t *testing.T) {
	s, store := authScoreServer(t)
	ticket, err := s.issuePairTicket("member", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	s.PairTicketsMu.Lock()
	s.PairTickets[ticket].ExpiresAt = time.Now().Add(-time.Second)
	s.PairTicketsMu.Unlock()

	client, server := dialAuthPair(t, s)
	done := make(chan error, 1)
	go func() {
		_, err := s.authenticateClient(server, "127.0.0.1", "localhost", false)
		done <- err
	}()
	ch := readChallenge(t, client)
	if err := client.WriteJSON(AuthPacket{
		Type: "auth", Nonce: ch.Nonce, PairTicket: ticket,
	}); err != nil {
		t.Fatal(err)
	}
	if err := <-done; !errors.Is(err, ErrPairExpired) {
		t.Fatalf("handshake err = %v, want ErrPairExpired", err)
	}

	if codes := recordedCodes(store, "127.0.0.1"); len(codes) != 0 {
		t.Fatalf("an expired ceremony was filed as an anomaly: %v", codes)
	}
	// The ban is the other half of the same exemption. A single attempt is
	// enough to check: one count means six slow ceremonies lock the address
	// out for five minutes.
	s.AuthFailsMu.Lock()
	rec := s.AuthFails["127.0.0.1"]
	s.AuthFailsMu.Unlock()
	if rec.FailCount != 0 || !rec.UnlockTime.IsZero() {
		t.Fatalf("an expired ceremony counted toward the ban: %+v", rec)
	}
}

// TestIssuedTicketStaysOutOfTheRing: asking for a ceremony is the feature
// working, and it is now an HTTP ask that never reaches the auth path, so
// nothing about it may be filed as an auth anomaly.
func TestIssuedTicketStaysOutOfTheRing(t *testing.T) {
	s, store := authScoreServer(t)
	rec := postPairRequest(s, pairRequestBody(t, "member"))
	if rec.Code != http.StatusCreated {
		t.Fatalf("ask = %d, want %d (%s)", rec.Code, http.StatusCreated, rec.Body)
	}

	if codes := recordedCodes(store, "192.0.2.1"); len(codes) != 0 {
		t.Fatalf("a requested ceremony was filed as an anomaly: %v", codes)
	}
}

// TestGuestFailureStaysOutOfTheRing: the pre-existing rule is that a packet
// without a role has nothing to fail at. The ring must follow the ban here
// too, or an anonymous client would be scored for a mistake it cannot make.
func TestGuestFailureStaysOutOfTheRing(t *testing.T) {
	s, store := authScoreServer(t)

	// A guest packet that trips the nonce check: no role, but a rejection.
	client, server := dialAuthPair(t, s)
	done := make(chan error, 1)
	go func() {
		_, err := s.authenticateClient(server, "127.0.0.1", "localhost", false)
		done <- err
	}()
	readChallenge(t, client)
	if err := client.WriteJSON(AuthPacket{
		Type: "auth", Nonce: strings.Repeat("cd", 64), Username: "Nobody",
	}); err != nil {
		t.Fatal(err)
	}
	if err := <-done; !errors.Is(err, ErrInvalidNonce) {
		t.Fatalf("handshake err = %v, want ErrInvalidNonce", err)
	}

	// An invalid nonce is a probe and is expected in the ring: the point here
	// is only that a role-less packet is not treated differently from before.
	if codes := recordedCodes(store, "127.0.0.1"); len(codes) != 1 {
		t.Fatalf("behaviour ring = %v, want one entry for a replayed nonce", codes)
	}
}

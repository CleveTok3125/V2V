package main

import (
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/CleveTok3125/V2V/internal/serverconfig"
)

// withPairCap installs an abuse config carrying the given ceiling, so the
// memory bound can be exercised without minting a thousand tickets.
func withPairCap(t *testing.T, cap int) {
	t.Helper()
	old := Cfg.Abuse.Load()
	Cfg.Abuse.Store(&serverconfig.AbuseConfig{PairMaxPending: cap})
	t.Cleanup(func() { Cfg.Abuse.Store(old) })
}

// TestIssuePairTicketBindsRoleAndAddress pins the property the whole flow
// rests on: a ticket names exactly one role and one address. Anything
// looser would let a leaked link be spent from somewhere else.
func TestIssuePairTicketBindsRoleAndAddress(t *testing.T) {
	withPairCap(t, 16)
	s := &ChatServer{PairTickets: map[string]*pairTicket{}}

	ticket, err := s.issuePairTicket("member", "203.0.113.7")
	if err != nil {
		t.Fatalf("issuePairTicket: %v", err)
	}
	if len(ticket) != pairTicketBytes*2 || strings.Trim(ticket, "0123456789abcdef") != "" {
		t.Fatalf("ticket is not %d lowercase hex chars: %q", pairTicketBytes*2, ticket)
	}
	got, ok := s.pairTicketFor(ticket, "203.0.113.7")
	if !ok {
		t.Fatal("the issued ticket does not resolve from its own address")
	}
	if got.Role != "member" || got.IP != "203.0.113.7" || got.Verified {
		t.Fatalf("ticket = %+v, want member from 203.0.113.7 and unverified", got)
	}
	if time.Until(got.ExpiresAt) <= 0 || time.Until(got.ExpiresAt) > pairTicketTTL {
		t.Fatalf("expires in %v, want within (0, %v]", time.Until(got.ExpiresAt), pairTicketTTL)
	}
}

// TestPairTicketIsUnguessable: the ticket is the whole capability. It
// travels in a URL, so a short or repeating value would let anyone who saw
// one link compete for a fresh desktop login.
func TestPairTicketIsUnguessable(t *testing.T) {
	withPairCap(t, 64)
	s := &ChatServer{PairTickets: map[string]*pairTicket{}}
	seen := make(map[string]bool)
	for i := 0; i < 64; i++ {
		ticket, err := s.issuePairTicket("member", "203.0.113.7")
		if err != nil {
			t.Fatalf("draw %d: %v", i, err)
		}
		if seen[ticket] {
			t.Fatalf("duplicate ticket %q after %d draws", ticket, i)
		}
		seen[ticket] = true
	}
}

// TestPairTicketRejectsAnotherAddress: a leaked link must be useless from
// anywhere but the machine that asked for it.
func TestPairTicketRejectsAnotherAddress(t *testing.T) {
	withPairCap(t, 16)
	s := &ChatServer{PairTickets: map[string]*pairTicket{}}
	ticket, err := s.issuePairTicket("member", "203.0.113.7")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := s.pairTicketFor(ticket, "198.51.100.9"); ok {
		t.Fatal("a ticket resolved from a different address")
	}
	if !s.markPairVerified(ticket) {
		t.Fatal("markPairVerified refused a live ticket")
	}
	if _, err := s.takeVerifiedPairTicket(ticket, "198.51.100.9"); err == nil {
		t.Fatal("a verified ticket was spent from a different address")
	}
}

// TestTakeRequiresTheProof: a ticket that no ceremony has proved must not
// become a session, or the browser half would be optional.
func TestTakeRequiresTheProof(t *testing.T) {
	withPairCap(t, 16)
	s := &ChatServer{PairTickets: map[string]*pairTicket{}}
	ticket, err := s.issuePairTicket("member", "203.0.113.7")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.takeVerifiedPairTicket(ticket, "203.0.113.7"); err == nil {
		t.Fatal("an unproved ticket was spent")
	}
}

// TestTakeSpendsOnce: the handshake that spends a ticket is one login. A
// replay of the same ticket must not produce a second session.
func TestTakeSpendsOnce(t *testing.T) {
	withPairCap(t, 16)
	s := &ChatServer{PairTickets: map[string]*pairTicket{}}
	ticket, err := s.issuePairTicket("member", "203.0.113.7")
	if err != nil {
		t.Fatal(err)
	}
	if !s.markPairVerified(ticket) {
		t.Fatal("markPairVerified refused a live ticket")
	}
	if _, err := s.takeVerifiedPairTicket(ticket, "203.0.113.7"); err != nil {
		t.Fatal("first spend refused")
	}
	if _, err := s.takeVerifiedPairTicket(ticket, "203.0.113.7"); err == nil {
		t.Fatal("a spent ticket was spent again")
	}
	if _, ok := s.pairTicketFor(ticket, "203.0.113.7"); ok {
		t.Fatal("a spent ticket still reads as live")
	}
}

// TestMarkVerifiedRefusesDeadTickets: a ceremony that finishes after the
// link expired, or after the ticket was spent, must not revive it.
func TestMarkVerifiedRefusesDeadTickets(t *testing.T) {
	withPairCap(t, 16)
	s := &ChatServer{PairTickets: map[string]*pairTicket{}}
	ticket, err := s.issuePairTicket("member", "203.0.113.7")
	if err != nil {
		t.Fatal(err)
	}
	s.PairTickets[ticket].ExpiresAt = time.Now().Add(-time.Second)
	if s.markPairVerified(ticket) {
		t.Fatal("an expired ticket was marked verified")
	}
	if s.markPairVerified("no-such-ticket") {
		t.Fatal("an unknown ticket was marked verified")
	}
}

// TestExpiredTicketIsRefusedAndDropped: a stale link must fail immediately
// rather than read as pending, and must not linger in the store.
func TestExpiredTicketIsRefusedAndDropped(t *testing.T) {
	withPairCap(t, 16)
	s := &ChatServer{PairTickets: map[string]*pairTicket{}}
	ticket, err := s.issuePairTicket("member", "203.0.113.7")
	if err != nil {
		t.Fatal(err)
	}
	s.PairTickets[ticket].ExpiresAt = time.Now().Add(-time.Second)
	if _, ok := s.pairTicketFor(ticket, "203.0.113.7"); ok {
		t.Fatal("an expired ticket resolved")
	}
	if s.countPairTickets() != 0 {
		t.Fatal("an expired ticket was left in the store")
	}
}

// TestIssueStopsAtTheMemoryCeiling: a ticket costs only memory, so the
// ceiling is what keeps an unbounded number of them from being minted.
func TestIssueStopsAtTheMemoryCeiling(t *testing.T) {
	withPairCap(t, 2)
	s := &ChatServer{PairTickets: map[string]*pairTicket{}}
	for i := 0; i < 2; i++ {
		if _, err := s.issuePairTicket("member", "203.0.113.7"); err != nil {
			t.Fatalf("ticket %d: %v", i, err)
		}
	}
	if _, err := s.issuePairTicket("member", "203.0.113.7"); !errors.Is(err, ErrPairCapacity) {
		t.Fatalf("ticket past the ceiling = %v, want ErrPairCapacity", err)
	}

	// The sweep is what returns the ceiling after abandoned ceremonies.
	s.prunePairTickets(time.Now().Add(pairTicketTTL + time.Second))
	if _, err := s.issuePairTicket("member", "203.0.113.7"); err != nil {
		t.Fatalf("ticket after the sweep: %v", err)
	}
}

// TestPruneDropsOnlyTheExpired: the sweep must not take live tickets with it.
func TestPruneDropsOnlyTheExpired(t *testing.T) {
	withPairCap(t, 16)
	s := &ChatServer{PairTickets: map[string]*pairTicket{}}
	live, err := s.issuePairTicket("member", "203.0.113.7")
	if err != nil {
		t.Fatal(err)
	}
	dead, err := s.issuePairTicket("member", "198.51.100.9")
	if err != nil {
		t.Fatal(err)
	}
	s.PairTickets[dead].ExpiresAt = time.Now().Add(-time.Second)

	s.prunePairTickets(time.Now())
	if _, ok := s.pairTicketFor(dead, "198.51.100.9"); ok {
		t.Fatal("an expired ticket survived the sweep")
	}
	if _, ok := s.pairTicketFor(live, "203.0.113.7"); !ok {
		t.Fatal("the sweep dropped a live ticket")
	}
}

// TestPairStoreConcurrent: minting runs on every accepted socket and the
// sweep runs on a timer, so the store is exercised under -race. The counts
// are what keep the test from passing by doing nothing.
func TestPairStoreConcurrent(t *testing.T) {
	withPairCap(t, 4096)
	s := &ChatServer{PairTickets: map[string]*pairTicket{}}
	var wg sync.WaitGroup
	var minted, spent atomic.Int64
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			ip := "203.0.113." + string(rune('a'+n%26))
			ticket, err := s.issuePairTicket("member", ip)
			if err != nil {
				return
			}
			minted.Add(1)
			if !s.markPairVerified(ticket) {
				return
			}
			if _, err := s.takeVerifiedPairTicket(ticket, ip); err == nil {
				spent.Add(1)
			}
		}(i)
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 8; i++ {
			s.prunePairTickets(time.Now())
		}
	}()
	wg.Wait()

	if minted.Load() == 0 {
		t.Fatal("no ticket was minted; the storm never reached the store")
	}
	if spent.Load() != minted.Load() {
		t.Errorf("spent %d of %d minted tickets", spent.Load(), minted.Load())
	}
}

// TestIssueReclaimsExpiredTickets: the ceiling bounds memory, and a ticket
// that ran out of time is not memory worth bounding. If dead tickets keep
// their place, a burst that fills the ceiling refuses new ceremonies for
// the whole sweep interval, which is longer than the ticket TTL.
func TestIssueReclaimsExpiredTickets(t *testing.T) {
	s := &ChatServer{PairTickets: map[string]*pairTicket{}}
	withPairCap(t, 2)

	for i := 0; i < 2; i++ {
		if _, err := s.issuePairTicket("member", "127.0.0.1"); err != nil {
			t.Fatalf("issue %d: %v", i, err)
		}
	}
	if _, err := s.issuePairTicket("member", "203.0.113.9"); !errors.Is(err, ErrPairCapacity) {
		t.Fatalf("a full ceiling accepted a third ticket: %v", err)
	}

	// Age both past their TTL, the way an abandoned ceremony leaves them.
	s.PairTicketsMu.Lock()
	for _, ticket := range s.PairTickets {
		ticket.ExpiresAt = time.Now().Add(-time.Second)
	}
	s.PairTicketsMu.Unlock()

	if _, err := s.issuePairTicket("member", "203.0.113.9"); err != nil {
		t.Fatalf("expired tickets still held the ceiling: %v", err)
	}
}

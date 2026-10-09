package main

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestNewPairSessionBindsTheCaller pins the property the whole desktop
// pairing flow rests on: a ticket names exactly one role, one client IP
// and one waiter. Anything looser would let a second connection ride in
// on the first one's ceremony.
func TestNewPairSessionBindsTheCaller(t *testing.T) {
	s := &ChatServer{}
	ticket, sess, err := s.newPairSession("member", "203.0.113.7")
	if err != nil {
		t.Fatalf("newPairSession: %v", err)
	}
	if len(ticket) != pairTicketBytes*2 {
		t.Fatalf("ticket = %q (%d chars), want %d hex chars", ticket, len(ticket), pairTicketBytes*2)
	}
	if sess.Role != "member" || sess.IP != "203.0.113.7" {
		t.Fatalf("session not bound to caller: %+v", sess)
	}
	if time.Until(sess.ExpiresAt) <= 0 || time.Until(sess.ExpiresAt) > pairTicketTTL {
		t.Fatalf("expires %v, want within (0, %v]", time.Until(sess.ExpiresAt), pairTicketTTL)
	}

	got, ok := s.claimPairSession(ticket)
	if !ok || got != sess {
		t.Fatalf("lookup(%q) = %v, %v; want the same session", ticket, got, ok)
	}
	// A different ticket must not reach this session. The replacement has to
	// differ from the current character: flipping the last hex digit to a
	// fixed "0" leaves the ticket unchanged one time in sixteen, which is how
	// this assertion used to fail at random.
	flipped := ticket[:len(ticket)-1]
	if ticket[len(ticket)-1] == '0' {
		flipped += "1"
	} else {
		flipped += "0"
	}
	if other, ok := s.claimPairSession(flipped); ok {
		t.Fatalf("a different ticket resolved to role %q; tickets are not unique", other.Role)
	}
}

// TestPairTicketIsUnguessable: the ticket is the challenge a browser signs
// and travels in a URL. A short or repeating ticket would let anyone who
// saw one link replay the ceremony against a fresh desktop login.
func TestPairTicketIsUnguessable(t *testing.T) {
	s := &ChatServer{}
	seen := make(map[string]bool)
	for i := 0; i < 64; i++ {
		// One client IP per draw so the per-IP cap is not what stops it.
		ticket, _, err := s.newPairSession("member", fmt.Sprintf("198.51.100.%d", i%200))
		if err != nil {
			t.Fatalf("draw %d: %v", i, err)
		}
		if seen[ticket] {
			t.Fatalf("duplicate ticket %q after %d draws", ticket, i)
		}
		if strings.Trim(ticket, "0123456789abcdef") != "" {
			t.Fatalf("ticket is not lowercase hex: %q", ticket)
		}
		seen[ticket] = true
	}
}

// TestResolvePairSessionHandsOffOnce: the ceremony and the handshake run
// on different goroutines, and the waiter may already have given up when
// a late submission lands. Resolving twice must not block the HTTP
// handler, and must not turn a granted session into a second grant.
func TestResolvePairSessionHandsOffOnce(t *testing.T) {
	s := &ChatServer{}
	_, sess, err := s.newPairSession("member", "203.0.113.7")
	if err != nil {
		t.Fatalf("newPairSession: %v", err)
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		sess.resolve(pairVerdict{OK: true})
		sess.resolve(pairVerdict{OK: false, Err: ErrPasskeyDisabled})
	}()

	if v := sess.await(pairTicketTTL); !v.OK || v.Err != nil {
		t.Fatalf("verdict = %+v, want the granted verdict", v)
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("a second resolve blocked instead of being dropped")
	}
}

// TestPairTicketIsSingleUse: the ticket reaches a browser over a URL, so
// the same link may be submitted more than once. Only the first submission
// may claim the pairing; the rest must look like an unknown ticket rather
// than resolve the handshake a second time.
func TestPairTicketIsSingleUse(t *testing.T) {
	s := &ChatServer{}
	ticket, _, err := s.newPairSession("member", "203.0.113.7")
	if err != nil {
		t.Fatalf("newPairSession: %v", err)
	}
	if _, ok := s.claimPairSession(ticket); !ok {
		t.Fatal("claim refused")
	}
	if !s.commitPairSession(ticket) {
		t.Fatal("the first commit refused")
	}
	// Replaying the link must not ride in on the login that just happened.
	if s.commitPairSession(ticket) {
		t.Fatal("a ticket was committed twice: one login, two grants")
	}
	if s.commitPairSession(ticket) {
		t.Fatal("a consumed ticket came back")
	}
}

// TestPairSessionAwaitTimesOut: a person who never opens the link must not
// hold a socket open indefinitely, and the waiter needs to learn it
// expired rather than block forever.
func TestPairSessionAwaitTimesOut(t *testing.T) {
	s := &ChatServer{}
	_, sess, err := s.newPairSession("member", "203.0.113.7")
	if err != nil {
		t.Fatalf("newPairSession: %v", err)
	}
	start := time.Now()
	v := sess.await(50 * time.Millisecond)
	if v.OK {
		t.Fatalf("await resolved without a submission: %+v", v)
	}
	if !errors.Is(v.Err, ErrPairExpired) {
		t.Fatalf("timeout err = %v, want ErrPairExpired", v.Err)
	}
	if elapsed := time.Since(start); elapsed < 40*time.Millisecond {
		t.Fatalf("await returned after %v, want it to wait out the budget", elapsed)
	}
}

// TestPairSessionAwaitTakesASubmittedFailure: /pair/submit reports the
// ceremony's own verdict, so a rejected assertion has to reach the desktop
// as a failure instead of looking like a timeout.
func TestPairSessionAwaitTakesASubmittedFailure(t *testing.T) {
	s := &ChatServer{}
	_, sess, err := s.newPairSession("member", "203.0.113.7")
	if err != nil {
		t.Fatalf("newPairSession: %v", err)
	}
	sess.resolve(pairVerdict{OK: false, Err: ErrVerificationFailed})
	v := sess.await(pairTicketTTL)
	if v.OK || !errors.Is(v.Err, ErrVerificationFailed) {
		t.Fatalf("verdict = %+v, want the submitted failure", v)
	}
}

// TestPairSessionCapsPending: each pending pairing pins a pre-auth socket
// for the whole ceremony, so the number in flight is capped globally and
// per client IP. Without the caps one client could park sockets until the
// server ran out of connections.
func TestPairSessionCapsPending(t *testing.T) {
	s := &ChatServer{}

	for i := 0; i < maxPendingPairsPerIP; i++ {
		if _, _, err := s.newPairSession("member", "203.0.113.7"); err != nil {
			t.Fatalf("pairing %d from one IP: %v", i, err)
		}
	}
	if _, _, err := s.newPairSession("member", "203.0.113.7"); !errors.Is(err, ErrPairRateLimited) {
		t.Fatalf("pairing past the per-IP cap = %v, want ErrPairRateLimited", err)
	}

	// Spread across clients the budget lasts until the global cap, not the
	// per-IP one.
	held := maxPendingPairsPerIP
	for i := held; i < maxPendingPairs; i++ {
		if _, _, err := s.newPairSession("member", fmt.Sprintf("198.51.100.%d", i)); err != nil {
			t.Fatalf("pairing %d from a fresh IP: %v", i, err)
		}
		held++
	}
	if held != maxPendingPairs {
		t.Fatalf("held %d pairings, want exactly %d", held, maxPendingPairs)
	}
	if _, _, err := s.newPairSession("member", "198.51.100.250"); !errors.Is(err, ErrPairRateLimited) {
		t.Fatalf("pairing past the global cap = %v, want ErrPairRateLimited", err)
	}

	// Finishing a pairing hands its slot back, so the cap is a concurrency
	// limit rather than a quota for the process lifetime.
	ticket, _, err := s.newPairSession("member", "198.51.100.251")
	if err != nil {
		// Saturated above; free one slot first.
		s.prunePairSessions(time.Now().Add(pairTicketTTL + time.Second))
		ticket, _, err = s.newPairSession("member", "198.51.100.251")
		if err != nil {
			t.Fatalf("pairing after the sweep: %v", err)
		}
	}
	if _, ok := s.claimPairSession(ticket); !ok {
		t.Fatal("fresh pairing refused right after the sweep")
	}
	if _, _, err := s.newPairSession("member", "198.51.100.252"); err != nil {
		t.Fatalf("a claimed pairing still holds the budget: %v", err)
	}
}

// TestPrunePairSessionsDropsExpired: abandoned pairings have to be swept,
// otherwise the cap above turns permanent after a busy afternoon.
func TestPrunePairSessionsDropsExpired(t *testing.T) {
	s := &ChatServer{}
	live, _, err := s.newPairSession("member", "203.0.113.7")
	if err != nil {
		t.Fatalf("newPairSession: %v", err)
	}
	dead, _, err := s.newPairSession("member", "198.51.100.9")
	if err != nil {
		t.Fatalf("newPairSession: %v", err)
	}
	// Reach into the map rather than through lookup, so the one-shot latch
	// stays untouched and the sweep is what decides the outcome.
	if raw, ok := s.PairSessions.Load(dead); ok {
		deadSess := raw.(*pairSession)
		s.PairSessions.Delete(dead)
		deadSess.ExpiresAt = time.Now().Add(-time.Second)
		s.PairSessions.Store(dead, deadSess)
	}

	s.prunePairSessions(time.Now())
	if _, ok := s.claimPairSession(dead); ok {
		t.Fatal("an expired pairing survived the sweep")
	}
	if _, ok := s.claimPairSession(live); !ok {
		t.Fatal("the sweep dropped a live pairing")
	}
}

// TestPairSessionsConcurrent: pairing is reached from every accepted
// socket and from the sweep timer, so the store is exercised under -race.
func TestPairSessionsConcurrent(t *testing.T) {
	s := &ChatServer{}
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			ip := "203.0.113." + string(rune('a'+n%26))
			ticket, sess, err := s.newPairSession("member", ip)
			if err != nil {
				return
			}
			sess.resolve(pairVerdict{OK: true})
			if _, ok := s.claimPairSession(ticket); ok {
				s.PairSessions.Delete(ticket)
			}
		}(i)
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 8; i++ {
			s.prunePairSessions(time.Now())
		}
	}()
	wg.Wait()
}

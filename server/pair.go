package main

// Desktop passkey pairing. A desktop client cannot run a WebAuthn
// ceremony: the private key lives inside an authenticator the process
// has no access to. So the desktop asks for a pair ticket, prints the
// page URL for that ticket and waits with its socket open; a browser
// anywhere completes the ceremony and posts the assertion back. The
// server verifies that assertion and resolves the waiting handshake.
//
// The session is owned by the HandleAuth call that created it, and it is
// reachable only through an unguessable ticket. That is what keeps a
// second connection from riding in on someone else's ceremony: the
// ticket is the capability, and it dies with the handshake that minted it.

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sync/atomic"
	"time"
)

const (
	// pairTicketTTL is how long a person has to open the link and confirm
	// the passkey. It is long because a phone has to wake up, unlock and
	// find the authenticator.
	pairTicketTTL = 5 * time.Minute

	// pairTicketBytes matches the handshake nonce, so the challenge both
	// sides derive from it is already oversized for preimage resistance.
	pairTicketBytes = 64

	// Every pending pairing pins a pre-auth socket for the whole
	// ceremony, so the count in flight is capped. The sockets are the
	// expensive part; the bookkeeping is free.
	maxPendingPairs = 64

	// One client may not hold the whole global budget, or the pairing
	// feature becomes a way to occupy every slot from a single address.
	maxPendingPairsPerIP = 4
)

var (
	ErrPairRateLimited = errors.New("auth_error: pair_rate_limited")
	ErrPairExpired     = errors.New("auth_error: pair_expired")
	ErrPairUnknown     = errors.New("auth_error: pair_unknown")
	ErrPairUsed        = errors.New("auth_error: pair_used")
)

// pairVerdict is what the browser's assertion turns into. A failure keeps
// its own error so the desktop can say what the ceremony rejected rather
// than reporting a generic timeout.
type pairVerdict struct {
	OK  bool
	Err error
}

// pairSession is one in-flight pairing. Only its own HandleAuth goroutine
// reads verdict; /pair/submit writes it exactly once.
type pairSession struct {
	ExpiresAt time.Time
	Role      string
	IP        string
	verdict   chan pairVerdict
	// consumed is the one-shot latch. A ceremony may claim the pairing
	// exactly once, so a replayed submission is refused as unknown.
	consumed atomic.Bool
}

// resolve hands the ceremony's verdict to the waiting handshake. It never
// blocks: the waiter may already have timed out, and a duplicate
// submission must not pin the HTTP handler.
func (p *pairSession) resolve(v pairVerdict) {
	select {
	case p.verdict <- v:
	default:
	}
}

// await blocks for budget, then reports expiry. A buffered channel of one
// keeps a resolution that arrives just after the deadline from being lost.
func (p *pairSession) await(budget time.Duration) pairVerdict {
	select {
	case v := <-p.verdict:
		return v
	case <-time.After(budget):
		return pairVerdict{OK: false, Err: ErrPairExpired}
	}
}

// newPairSession mints a ticket and its session for one desktop handshake.
func (s *ChatServer) newPairSession(role, clientIP string) (string, *pairSession, error) {
	if s.countPairSessions() >= maxPendingPairs {
		return "", nil, ErrPairRateLimited
	}
	if s.countPairSessionsForIP(clientIP) >= maxPendingPairsPerIP {
		return "", nil, ErrPairRateLimited
	}
	raw := make([]byte, pairTicketBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", nil, ErrEntropyExhaustion
	}
	ticket := hex.EncodeToString(raw)
	sess := &pairSession{
		ExpiresAt: time.Now().Add(pairTicketTTL),
		Role:      role,
		IP:        clientIP,
		verdict:   make(chan pairVerdict, 1),
	}
	s.PairSessions.Store(ticket, sess)
	return ticket, sess, nil
}

// lookupPairSession returns a live, unconsumed session for a ticket.
// Expired and already-resolved sessions are removed here so a second
// attempt with the same link fails like an unknown one.
func (s *ChatServer) lookupPairSession(ticket string) (*pairSession, bool) {
	raw, ok := s.PairSessions.Load(ticket)
	if !ok {
		return nil, false
	}
	sess, ok := raw.(*pairSession)
	if !ok {
		s.PairSessions.Delete(ticket)
		return nil, false
	}
	if time.Now().After(sess.ExpiresAt) {
		s.PairSessions.Delete(ticket)
		return nil, false
	}
	if sess.consumed.Swap(true) {
		s.PairSessions.Delete(ticket)
		return nil, false
	}
	return sess, true
}

// countPairSessions and countPairSessionsForIP walk the store. The map is
// capped at maxPendingPairs entries, so this stays cheap enough to run on
// every pairing request.
func (s *ChatServer) countPairSessions() int {
	n := 0
	s.PairSessions.Range(func(_, _ any) bool {
		n++
		return true
	})
	return n
}

func (s *ChatServer) countPairSessionsForIP(clientIP string) int {
	n := 0
	s.PairSessions.Range(func(_, v any) bool {
		if sess, ok := v.(*pairSession); ok && sess.IP == clientIP {
			n++
		}
		return true
	})
	return n
}

// prunePairSessions drops pairings nobody finished. Called from the same
// periodic sweep that expires handshake nonces, so an abandoned ceremony
// returns its slot to the cap.
func (s *ChatServer) prunePairSessions(now time.Time) {
	s.PairSessions.Range(func(k, v any) bool {
		sess, ok := v.(*pairSession)
		if !ok || now.After(sess.ExpiresAt) {
			s.PairSessions.Delete(k)
		}
		return true
	})
}

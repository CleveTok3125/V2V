package main

// Desktop passkey pairing. A desktop client cannot run a WebAuthn
// ceremony: the private key lives inside an authenticator the process
// cannot reach. So the desktop asks for a pair ticket, prints the page
// URL for it, and a browser anywhere completes the ceremony.
//
// Waiting is the human's job and can take minutes, so it happens over
// poll requests that hold nothing: a socket held for the whole ceremony
// would cost a connection slot. The ticket is unguessable, single-use
// and tied to the asking address.
//
// See docs/TECHNICAL.md, "Desktop pairing", for the request flow.

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"time"

	"github.com/CleveTok3125/V2V/internal/serverconfig"
)

// pairTicketTTL is how long a person has to open the link and confirm.
// Generous: a phone must wake, unlock and find the authenticator. Cost
// of holding a ticket that long is a few hundred bytes, not a connection.
const pairTicketTTL = 5 * time.Minute

// pairTicketBytes is the ticket's entropy. The challenge a browser signs is
// SHA-256 of this value, so the digest decides preimage resistance and this
// only has to make the ticket unguessable: 256 bits does that, and a shorter
// ticket keeps the page URL small enough to render as a scannable QR code.
const pairTicketBytes = 32

var (
	// ErrPairCapacity is the memory guard. A ticket costs nothing to hold
	// but nothing is free, and the only thing standing between a client
	// and an unlimited number of them is this ceiling.
	ErrPairCapacity = errors.New("auth_error: pair_capacity")

	// ErrPairExpired reports that a link ran out of time, so the desktop
	// can say so instead of showing a blank failure.
	ErrPairExpired = errors.New("auth_error: pair_expired")

	// ErrPairUnknown covers a ticket that is unknown, spent or no longer
	// live. They are one answer on purpose: telling them apart would let a
	// caller probe for tickets that exist.
	ErrPairUnknown = errors.New("auth_error: pair_unknown")
)

// notePairRejection logs a refused ceremony. The ticket survives on
// purpose: the person may simply be holding the wrong passkey, and the
// link should let them try another one.
func (s *ChatServer) notePairRejection(clientIP string) {
	logWarnf("⚠️ (pair,submit,fail) %s: bằng chứng không xác minh được, vé vẫn dùng được.", clientIP)
}

// pairMaxPending reads the memory ceiling. Tests build a bare server with
// no config loaded, so the config default is the fallback rather than zero,
// which would refuse every pairing.
func pairMaxPending() int {
	if a := Cfg.Abuse.Load(); a != nil && a.PairMaxPending > 0 {
		return a.PairMaxPending
	}
	return serverconfig.DefaultPairMaxPending
}

// pairTicket is one outstanding pairing. Verified is set by /pair/submit
// once a browser has proved the role; Consumed is set by the handshake that
// spends it. Both are guarded by the store's own locking, so the type
// carries plain fields read under PairTicketsMu.
type pairTicket struct {
	ExpiresAt time.Time
	Role      string
	IP        string
	Verified  bool
	Consumed  bool
}

// issuePairTicket mints a ticket for one desktop. The caller has already
// confirmed the role exists, so the role and the address are both settled
// by the time anything is stored.
func (s *ChatServer) issuePairTicket(role, clientIP string) (string, error) {
	s.PairTicketsMu.Lock()
	defer s.PairTicketsMu.Unlock()

	// Reclaim before measuring. The sweep runs on the same ticker as the
	// nonce sweep, but a ticket that ran out of time is no longer memory
	// worth bounding, and letting it keep its place would refuse new
	// ceremonies for the rest of the interval.
	s.prunePairTicketsLocked(time.Now())
	if len(s.PairTickets) >= pairMaxPending() {
		return "", ErrPairCapacity
	}
	raw := make([]byte, pairTicketBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", ErrEntropyExhaustion
	}
	ticket := hex.EncodeToString(raw)
	s.PairTickets[ticket] = &pairTicket{
		ExpiresAt: time.Now().Add(pairTicketTTL),
		Role:      role,
		IP:        clientIP,
	}
	return ticket, nil
}

// pairTicketFor answers the HTTP endpoints, which collapse every refusal
// into one answer on purpose: telling them apart in a response would let a
// caller probe for tickets that exist.
func (s *ChatServer) pairTicketFor(ticket, clientIP string) (*pairTicket, bool) {
	t, err := s.pairTicketState(ticket, clientIP)
	return t, err == nil
}

// pairTicketState returns a live ticket owned by the caller, and why it
// refused. Only the login path needs the reason: a link that ran out of time
// and a link that was never issued are different events, and filing both as
// a probe penalises a person for taking too long.
func (s *ChatServer) pairTicketState(ticket, clientIP string) (*pairTicket, error) {
	s.PairTicketsMu.Lock()
	defer s.PairTicketsMu.Unlock()
	return s.livePairTicketLocked(ticket, clientIP)
}

func (s *ChatServer) livePairTicketLocked(ticket, clientIP string) (*pairTicket, error) {
	t, ok := s.PairTickets[ticket]
	if !ok || t.Consumed {
		return nil, ErrPairUnknown
	}
	if time.Now().After(t.ExpiresAt) {
		delete(s.PairTickets, ticket)
		return nil, ErrPairExpired
	}
	if t.IP != clientIP {
		return nil, ErrPairUnknown
	}
	return t, nil
}

// markPairVerified records that a browser proved the role for this ticket.
// A rejected assertion deliberately does not clear a previous success and
// does not spend the ticket: the person may simply be holding the wrong
// passkey and should be able to try another one on the same link.
func (s *ChatServer) markPairVerified(ticket string) bool {
	s.PairTicketsMu.Lock()
	defer s.PairTicketsMu.Unlock()
	t, ok := s.PairTickets[ticket]
	if !ok || t.Consumed || time.Now().After(t.ExpiresAt) {
		return false
	}
	t.Verified = true
	return true
}

// takeVerifiedPairTicket spends a ticket and reports why it could not. It
// is the only way a ticket turns into a session: proof, same address
// and one use, so a leaked ticket still cannot be spent from elsewhere.
//
// An unverified ticket is not "expired": the browser simply has not
// finished, the ordinary state while a person finds their passkey.
func (s *ChatServer) takeVerifiedPairTicket(ticket, clientIP string) (*pairTicket, error) {
	s.PairTicketsMu.Lock()
	defer s.PairTicketsMu.Unlock()
	t, err := s.livePairTicketLocked(ticket, clientIP)
	if err != nil {
		return nil, err
	}
	if !t.Verified {
		return nil, ErrPairUnknown
	}
	t.Consumed = true
	return t, nil
}

// countPairTickets reports the store size. The sweep keeps it near the
// number of live pairings, so this is a cheap read of a small map.
func (s *ChatServer) countPairTickets() int {
	s.PairTicketsMu.Lock()
	defer s.PairTicketsMu.Unlock()
	return len(s.PairTickets)
}

// prunePairTickets drops tickets nobody finished. Called from the same
// periodic sweep that expires handshake nonces, so an abandoned link
// returns its share of the memory ceiling.
func (s *ChatServer) prunePairTickets(now time.Time) {
	s.PairTicketsMu.Lock()
	defer s.PairTicketsMu.Unlock()
	s.prunePairTicketsLocked(now)
}

// prunePairTicketsLocked is the body the sweep and the issue path share.
// Caller must hold PairTicketsMu.
func (s *ChatServer) prunePairTicketsLocked(now time.Time) {
	for ticket, t := range s.PairTickets {
		if now.After(t.ExpiresAt) {
			delete(s.PairTickets, ticket)
		}
	}
}

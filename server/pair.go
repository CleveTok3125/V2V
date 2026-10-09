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
	"fmt"
	"sync/atomic"
	"time"

	"github.com/CleveTok3125/V2V/internal/strutil"
	"github.com/CleveTok3125/V2V/internal/wire"

	"github.com/gorilla/websocket"
)

// awaitPairAssertion runs the browser half of a desktop passkey login.
// The client has already proved it holds a live handshake nonce; from here
// the socket simply waits while a browser signs the pair ticket.
func (s *ChatServer) awaitPairAssertion(conn *websocket.Conn, clientIP string, req AuthPacket, onion bool) (Permission, AuthPacket, error) {
	perms := GetDefaultPermission()
	if !WAConfig.Enabled {
		logWarnf("⚠️ [AUTH FAIL] %s: passkey bị tắt (thiếu WEBAUTHN_RPID/ORIGIN).", clientIP)
		return perms, req, fmt.Errorf("%w", ErrPasskeyDisabled)
	}
	if passkeyDisabledForOnion(onion) {
		logWarnf("⚠️ [AUTH FAIL] %s: passkey bị tắt trên onion (ONION_ALLOW_PASSKEY=false).", clientIP)
		return perms, req, fmt.Errorf("%w", ErrPasskeyDisabled)
	}
	// The role is named by the client and confirmed here, so the browser
	// page never gets to decide which role the ceremony is for.
	if req.Role == "" {
		return perms, req, fmt.Errorf("%w", ErrInvalidRole)
	}
	s.RoleRegistryMu.RLock()
	roleDef, exists := s.RoleRegistry[req.Role]
	s.RoleRegistryMu.RUnlock()
	if !exists {
		logWarnf("⚠️ [AUTH FAIL] %s: Yêu cầu Role không tồn tại [%s]", clientIP, req.Role)
		return perms, req, fmt.Errorf("%w", ErrInvalidRole)
	}

	ticket, sess, err := s.newPairSession(req.Role, clientIP)
	if err != nil {
		return perms, req, err
	}
	// The ticket rides in the URL fragment, which browsers never put in a
	// request line, so it stays out of server and proxy logs.
	if err := conn.WriteJSON(AuthPacket{
		Type:       wire.AuthPair,
		PairTicket: ticket,
		PairURL:    WAConfig.Origin + "/web/#pair=" + ticket,
		PairTTLSec: int(pairTicketTTL.Seconds()),
	}); err != nil {
		s.PairSessions.Delete(ticket)
		return perms, req, err
	}
	logInfof("🔗 [PAIR] %s yêu cầu ceremony cho role [%s], ticket=%s…", clientIP, req.Role, strutil.Short(ticket))

	verdict := sess.await(pairTicketTTL)
	s.PairSessions.Delete(ticket)
	if !verdict.OK {
		logWarnf("⚠️ [AUTH FAIL] %s: ceremony passkey thất bại: %v", clientIP, verdict.Err)
		return perms, req, verdict.Err
	}
	logInfof("✅ [AUTH SUCCESS] %s đăng nhập bằng passkey thật qua browser, role: [%s]", clientIP, req.Role)
	req.AuthType = "passkey"
	return roleDef.Permission, req, nil
}

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

// claimPairSession returns a live session for a ticket, without deciding the
// ticket's fate. A failed verification must leave the link usable, so the
// one-shot latch is set by commitPairSession instead of here.
func (s *ChatServer) claimPairSession(ticket string) (*pairSession, bool) {
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
	return sess, true
}

// commitPairSession spends a ticket once an assertion has been verified. The
// latch is what makes one login one login: a browser that reloads, or a link
// that is opened twice, cannot resolve the same handshake a second time.
func (s *ChatServer) commitPairSession(ticket string) bool {
	raw, ok := s.PairSessions.Load(ticket)
	if !ok {
		return false
	}
	sess, ok := raw.(*pairSession)
	if !ok {
		s.PairSessions.Delete(ticket)
		return false
	}
	if sess.consumed.Swap(true) {
		s.PairSessions.Delete(ticket)
		return false
	}
	return true
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

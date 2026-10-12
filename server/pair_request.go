package main

// The desktop's ask for a pairing ticket. It is HTTP rather than a websocket
// dial for a reason the connection cooldown enforces: a desktop logs in
// twice, once to ask and once to spend, and two dials from one address
// inside CONNECTION_COOLDOWN are refused as churn. Keeping the ask off the
// socket leaves the desktop exactly one dial, the one that becomes a session.

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/CleveTok3125/V2V/internal/guard"
	"github.com/CleveTok3125/V2V/internal/strutil"
)

// pairRequestCooldown bounds how often one address may ask. A desktop asks
// once per attempt, so this only slows callers that are not logging in; the
// pending ceiling, not this, is what bounds the store.
const pairRequestCooldown = 5 * time.Second

var pairRequestCooldowns = guard.NewCooldownMap()

type pairRequest struct {
	Role string `json:"role"`
}

type pairOffer struct {
	Ticket string `json:"ticket"`
	URL    string `json:"url"`
	TTLSec int    `json:"ttl_sec"`
}

// handlePairRequest mints a ticket for the named role and returns the link
// that proves it. The ticket is bound to the caller's address, so the link
// is useless to anyone who is not also the desktop that asked.
func (s *ChatServer) handlePairRequest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !WAConfig.Enabled {
		http.Error(w, "passkey disabled", http.StatusServiceUnavailable)
		return
	}
	clientIP := getClientIP(r)

	var req pairRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4*1024)).Decode(&req); err != nil {
		http.Error(w, "bad payload", http.StatusBadRequest)
		return
	}
	if req.Role == "" {
		http.Error(w, "missing role", http.StatusBadRequest)
		return
	}
	// The role is named by the desktop and confirmed here, so the browser
	// page never gets to decide which role the ceremony is for.
	s.RoleRegistryMu.RLock()
	_, exists := s.RoleRegistry[req.Role]
	s.RoleRegistryMu.RUnlock()
	if !exists {
		// No log line: this is a client-side typo on an endpoint anyone can
		// call, and the meter below is skipped for it, so logging here would
		// let one caller flood the log without ever being slowed down.
		http.Error(w, "unknown role", http.StatusNotFound)
		return
	}

	// Metered immediately before the store is touched, not before the cheap
	// checks: a mistyped role must come back as the error it is, not as
	// "too many attempts" that leaves the person waiting out a window for a
	// request that never reached the store.
	if !pairRequestCooldowns.Allow(clientIP, pairRequestCooldown) {
		http.Error(w, "too many attempts", http.StatusTooManyRequests)
		return
	}

	ticket, err := s.issuePairTicket(req.Role, clientIP)
	if err != nil {
		if errors.Is(err, ErrPairCapacity) {
			http.Error(w, "no room for another pairing", http.StatusServiceUnavailable)
			return
		}
		http.Error(w, "cannot issue a ticket", http.StatusInternalServerError)
		return
	}
	logInfof("🔗 (pair) %s nhận vé ceremony cho role [%s], ticket=%s…", clientIP, req.Role, strutil.Short(ticket))

	// The ticket rides in the fragment, which a browser never puts in a
	// request line, so it stays out of server and proxy logs.
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusCreated)
	writeJSON(w, pairOffer{
		Ticket: ticket,
		URL:    WAConfig.Origin + "/web/#pair=" + ticket,
		TTLSec: int(pairTicketTTL.Seconds()),
	})
}

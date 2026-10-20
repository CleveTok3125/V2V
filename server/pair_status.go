package main

// The desktop's poll for its pairing result. This replaces what used to be
// a socket held open for the whole ceremony: the request returns at once,
// so nothing is held between polls and an idle path cannot cut the
// ceremony short. See docs/TECHNICAL.md, "Desktop pairing".

import (
	"net/http"
	"time"

	"github.com/CleveTok3125/V2V/lib/guard"
)

// pairStatusCooldown bounds how often one address may poll. It sits below
// the client's own cadence so a well-behaved desktop is never refused,
// while a caller spinning on the endpoint is.
const pairStatusCooldown = time.Second

var pairStatusCooldowns = guard.NewCooldownMap()

// handlePairStatus answers whether a ceremony has proved the ticket yet.
// It is readable only from the address that asked for the ticket, so a
// third party holding a leaked link cannot learn anything from it.
func (s *ChatServer) handlePairStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	clientIP := getClientIP(r)
	if !pairStatusCooldowns.Allow(clientIP, pairStatusCooldown) {
		s.rejectCooldown(w, pairStatusCooldowns, clientIP, pairStatusCooldown, "too many attempts")
		return
	}
	ticket := r.URL.Query().Get("ticket")
	if ticket == "" {
		http.Error(w, "missing ticket", http.StatusBadRequest)
		return
	}
	t, ok := s.pairTicketFor(ticket, clientIP)
	if !ok {
		// Unknown, expired, spent and "wrong address" are one answer: telling
		// them apart would let a caller probe for tickets that exist.
		http.Error(w, "unknown or expired pairing", http.StatusGone)
		return
	}
	state := "pending"
	if t.Verified {
		state = "verified"
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, map[string]any{"state": state})
}

package main

// The browser half of a desktop passkey login. The page has just run
// navigator.credentials.get over the pair ticket and posts the assertion
// here; the handler verifies it against the role the desktop named and
// records the role as proved so the desktop's next poll can spend it.

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/CleveTok3125/V2V/lib/guard"
	"github.com/CleveTok3125/V2V/lib/strutil"
)

const pairSubmitCooldown = 10 * time.Second

var pairSubmitCooldowns = guard.NewCooldownMap()

// pairSubmission is the page's POST body. It carries no role on purpose:
// the role is a property of the ticket, and a value the browser
// can edit is a second, weaker way to say the same thing.
type pairSubmission struct {
	Ticket     string `json:"ticket"`
	PasskeyID  string `json:"passkey_id"`
	AuthData   string `json:"auth_data"`
	ClientData string `json:"client_data"`
	Sig        string `json:"sig"`
}

func (s *ChatServer) handlePairSubmit(w http.ResponseWriter, r *http.Request) {
	if s.WebAuthn == nil || !WAConfig.Enabled {
		http.Error(w, "passkey disabled", http.StatusServiceUnavailable)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	// Metered ahead of the store: this endpoint is unauthenticated, and
	// every attempt would otherwise be a file read. No IP binding — the
	// browser may be a phone on a different network — which is safe
	// because a submission can only ever be verified, never trusted.
	clientIP := getClientIP(r)
	if !pairSubmitCooldowns.Allow(clientIP, pairSubmitCooldown) {
		// The browser needs the number: it offers a retry button and must
		// know how long to keep it disabled, or the first press is refused
		// again and reads as an unexplained failure.
		setRetryAfter(w, pairSubmitCooldowns.RetryAfter(clientIP, pairSubmitCooldown))
		http.Error(w, "too many attempts", http.StatusTooManyRequests)
		return
	}

	var sub pairSubmission
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64*1024)).Decode(&sub); err != nil {
		http.Error(w, "bad payload", http.StatusBadRequest)
		return
	}
	if sub.Ticket == "" || sub.PasskeyID == "" || sub.AuthData == "" || sub.ClientData == "" || sub.Sig == "" {
		http.Error(w, "missing fields", http.StatusBadRequest)
		return
	}

	// The ticket is read, not spent: a rejected assertion must leave the
	// link usable so the person can pick another passkey instead of
	// restarting the client and the whole ceremony.
	ticket, ok := s.pairTicketFor(sub.Ticket, clientIP)
	if !ok {
		http.Error(w, "unknown or expired pairing", http.StatusGone)
		return
	}

	cred, ok := s.WebAuthn.Credential(ticket.Role, sub.PasskeyID)
	if !ok {
		logWarnf("⚠️ (pair,submit,fail) %s: credential không thuộc role [%s]", clientIP, ticket.Role)
		s.notePairRejection(clientIP)
		http.Error(w, "verification failed", http.StatusForbidden)
		return
	}
	counter, verr := verifyAssertionLib(cred, sub.Ticket, sub.AuthData, sub.ClientData, sub.Sig, ticket.Role)
	if verr != nil {
		logErrorf("🚨 (pair,submit,fail) %s: %v", clientIP, verr)
		s.notePairRejection(clientIP)
		http.Error(w, "verification failed", http.StatusForbidden)
		return
	}
	// Same clone-detection rule as the web login: a counter that does not
	// advance means the same authenticator answered twice, or a copy did.
	if counter != 0 && cred.SignCount != 0 && counter <= cred.SignCount {
		logErrorf("🚨 (pair,submit,fail) %s: counter không tăng (%d <= %d)", clientIP, counter, cred.SignCount)
		s.notePairRejection(clientIP)
		http.Error(w, "verification failed", http.StatusForbidden)
		return
	}
	if counter != 0 {
		if err := s.WebAuthn.UpdateSignCount(ticket.Role, cred.CredentialID, counter); err != nil {
			logErrorf("🚨 (pair,submit,fail) %s: không lưu counter: %v", clientIP, err)
			s.notePairRejection(clientIP)
			http.Error(w, "verification failed", http.StatusForbidden)
			return
		}
	}

	logInfof("✅ (pair,submit,success,ticket) %s xác thực được, role=[%s], ticket=%s…", clientIP, ticket.Role, strutil.Short(sub.Ticket))
	s.markPairVerified(sub.Ticket)
	// Content-Type before the status: WriteHeader snapshots the header map,
	// so setting it afterwards leaves the browser with text/plain.
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	writeJSON(w, map[string]any{"ok": true})
}

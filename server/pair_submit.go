package main

// The browser half of a desktop passkey login. The page has just run
// navigator.credentials.get over the pair ticket and posts the assertion
// here; the handler verifies it against the role the desktop named and
// resolves the handshake that is waiting for it.

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/CleveTok3125/V2V/internal/guard"
	"github.com/CleveTok3125/V2V/internal/strutil"
)

const pairSubmitCooldown = 10 * time.Second

var pairSubmitCooldowns = guard.NewCooldownMap()

// pairSubmission is the page's POST body. It carries no role on purpose:
// the role is a property of the pairing session, and a value the browser
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

	sess, ok := s.lookupPairSession(sub.Ticket)
	if !ok {
		// Unknown, expired or already claimed all look the same from out
		// here: the pairing is spent either way.
		http.Error(w, "unknown or spent pairing", http.StatusGone)
		return
	}

	cred, ok := s.WebAuthn.Credential(sess.Role, sub.PasskeyID)
	if !ok {
		logWarnf("⚠️ [PAIR SUBMIT] %s: credential không thuộc role [%s]", clientIP, sess.Role)
		sess.resolve(pairVerdict{OK: false, Err: ErrVerificationFailed})
		http.Error(w, "verification failed", http.StatusForbidden)
		return
	}
	counter, verr := verifyAssertionLib(cred, sub.Ticket, sub.AuthData, sub.ClientData, sub.Sig, sess.Role)
	if verr != nil {
		logErrorf("🚨 [PAIR SUBMIT] %s: %v", clientIP, verr)
		sess.resolve(pairVerdict{OK: false, Err: ErrVerificationFailed})
		http.Error(w, "verification failed", http.StatusForbidden)
		return
	}
	// Same clone-detection rule as the web login: a counter that does not
	// advance means the same authenticator answered twice, or a copy did.
	if counter != 0 && cred.SignCount != 0 && counter <= cred.SignCount {
		logErrorf("🚨 [PAIR SUBMIT] %s: counter không tăng (%d <= %d)", clientIP, counter, cred.SignCount)
		sess.resolve(pairVerdict{OK: false, Err: ErrCounterNotIncreasing})
		http.Error(w, "verification failed", http.StatusForbidden)
		return
	}
	if counter != 0 {
		if err := s.WebAuthn.UpdateSignCount(sess.Role, cred.CredentialID, counter); err != nil {
			logErrorf("🚨 [PAIR SUBMIT] %s: không lưu counter: %v", clientIP, err)
			sess.resolve(pairVerdict{OK: false, Err: ErrCounterNotIncreasing})
			http.Error(w, "verification failed", http.StatusForbidden)
			return
		}
	}

	logInfof("✅ [PAIR SUBMIT] %s xác thực được, role=[%s], ticket=%s…", clientIP, sess.Role, strutil.Short(sub.Ticket))
	sess.resolve(pairVerdict{OK: true})
	w.WriteHeader(http.StatusAccepted)
	writeJSON(w, map[string]any{"ok": true})
}

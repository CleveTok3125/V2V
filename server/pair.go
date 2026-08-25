package main

// Device-pairing endpoints for desktop passkey login. The desktop binary
// cannot run a WebAuthn ceremony itself, so it allocates a pair nonce, the
// user completes the assertion in any browser (possibly another device) on
// /web/#pair=<nonce>, and the desktop polls until the result shows up.

import (
	"encoding/json"
	"net/http"
	"time"
)

const (
	pairNonceTTL  = 5 * time.Minute
	pairResultTTL = 2 * time.Minute
)

type pairNewResponse struct {
	Nonce string `json:"nonce"`
	URL   string `json:"url"`
}

// handlePairNew allocates a pair nonce and returns the browser URL that runs
// the ceremony against it.
func (s *ChatServer) handlePairNew(w http.ResponseWriter, r *http.Request) {
	if !WAConfig.Enabled {
		http.Error(w, "passkey disabled", http.StatusServiceUnavailable)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	nonce, err := s.NewPairNonce(pairNonceTTL)
	if err != nil {
		http.Error(w, "entropy error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, pairNewResponse{
		Nonce: nonce,
		URL:   WAConfig.Origin + "/web/#pair=" + nonce,
	})
}

// pairSubmission is what the browser page posts back after a successful
// navigator.credentials.get() call.
type pairSubmission struct {
	Nonce     string `json:"nonce"`
	Role      string `json:"role"`
	PasskeyID string `json:"passkey_id"`

	AuthData   string `json:"auth_data"`
	ClientData string `json:"client_data"`
	Sig        string `json:"sig"`
}

func (s *ChatServer) handlePairSubmit(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var sub pairSubmission
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32*1024)).Decode(&sub); err != nil {
		http.Error(w, "bad payload", http.StatusBadRequest)
		return
	}
	if sub.Nonce == "" || sub.PasskeyID == "" {
		http.Error(w, "missing fields", http.StatusBadRequest)
		return
	}
	metaRaw, ok := s.ActiveNonces.Load(sub.Nonce)
	if !ok {
		http.Error(w, "unknown or expired nonce", http.StatusGone)
		return
	}
	meta := metaRaw.(NonceMeta)
	if !meta.PairMode || time.Now().After(meta.ExpiresAt) {
		s.ActiveNonces.Delete(sub.Nonce)
		http.Error(w, "nonce not pairable", http.StatusForbidden)
		return
	}

	s.PairResults.Store(sub.Nonce, sub)
	time.AfterFunc(pairResultTTL, func() { s.PairResults.Delete(sub.Nonce) })
	w.WriteHeader(http.StatusAccepted)
}

func (s *ChatServer) handlePairStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	nonce := r.URL.Query().Get("nonce")
	if nonce == "" {
		http.Error(w, "missing nonce", http.StatusBadRequest)
		return
	}
	raw, ok := s.PairResults.LoadAndDelete(nonce)
	if !ok {
		writeJSON(w, map[string]any{"pending": true})
		return
	}
	sub := raw.(pairSubmission)
	// The ActiveNonces entry is intentionally kept: it is consumed by
	// HandleAuth when the desktop presents this nonce during the WebSocket
	// handshake, keeping "one pair nonce = one login".
	writeJSON(w, map[string]any{
		"pending":             false,
		"role":                sub.Role,
		"passkey_id":          sub.PasskeyID,
		"passkey_auth_data":   sub.AuthData,
		"passkey_client_data": sub.ClientData,
		"passkey_sig":         sub.Sig,
	})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

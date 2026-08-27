package main

// HTTP endpoints for browser-side passkey enrollment. Access is gated by a
// one-time ticket the admin issued on the server host (see -enroll flag in
// main.go). The ceremony itself runs entirely in the member's browser; this
// server only binds the challenge to the ticket and stores the public half.

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"log"
	"net/http"
	"time"
)

const enrollChallengeTTL = 5 * time.Minute

type creationOptions struct {
	PublicKey creationOptionsPK `json:"publicKey"`
}

type creationOptionsPK struct {
	Challenge              string   `json:"challenge"`
	RP                     rpEnt    `json:"rp"`
	User                   userEnt  `json:"user"`
	PubKeyCredParams       []algEnt `json:"pubKeyCredParams"`
	AuthenticatorSelection authSel  `json:"authenticatorSelection"`
	Timeout                int64    `json:"timeout"`
	Attestation            string   `json:"attestation"`
}

type rpEnt struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type userEnt struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type algEnt struct {
	Type string `json:"type"`
	Alg  int    `json:"alg"`
}

type authSel struct {
	RequireResidentKey *bool  `json:"requireResidentKey,omitempty"`
	ResidentKey        string `json:"residentKey,omitempty"`
	UserVerification string `json:"userVerification"`
}

func randomB64url(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// handleEnrollBegin validates the ticket and returns creation options with a
// fresh challenge bound to it.
func (s *ChatServer) handleEnrollBegin(w http.ResponseWriter, r *http.Request) {
	if s.WebAuthn == nil {
		http.Error(w, "passkey disabled", http.StatusServiceUnavailable)
		return
	}
	if !WAConfig.Enabled {
		http.Error(w, "passkey disabled", http.StatusServiceUnavailable)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	code := r.URL.Query().Get("ticket")
	if code == "" {
		http.Error(w, "missing ticket", http.StatusBadRequest)
		return
	}
	s.WebAuthn.PruneExpired()
	log.Printf("🔐 [ENROLL BEGIN] ticket=%s… from=%s", shortCode(code), r.RemoteAddr)

	challenge, err := randomB64url(32)
	if err != nil {
		log.Printf("❌ [ENROLL BEGIN] ticket=%s… entropy error: %v", shortCode(code), err)
		http.Error(w, "entropy error", http.StatusInternalServerError)
		return
	}
	role, err := s.WebAuthn.BindChallenge(code, challenge)
	if err != nil {
		log.Printf("❌ [ENROLL BEGIN] ticket=%s… bind failed: %v", shortCode(code), err)
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	log.Printf("✅ [ENROLL BEGIN] ticket=%s… role=%s challenge=%s…", shortCode(code), role, challenge[:12])
	userID, _ := randomB64url(16)

	requireResidentKey := true
	writeJSON(w, creationOptions{
		PublicKey: creationOptionsPK{
			Challenge: challenge,
			RP:        rpEnt{ID: WAConfig.RPID, Name: "V2V"},
			User:      userEnt{ID: userID, Name: role + ":" + code[:8]},
			PubKeyCredParams: []algEnt{
				{Type: "public-key", Alg: -7},   // ES256
				{Type: "public-key", Alg: -257}, // RS256
			},
			AuthenticatorSelection: authSel{
				RequireResidentKey: &requireResidentKey,
				ResidentKey:        "required",
				UserVerification:   "preferred",
			},
			Timeout:     int64(enrollChallengeTTL.Seconds() * 1000),
			Attestation: "none",
		},
	})
	_ = role // role is already bound server-side via the ticket
}

type finishRequest struct {
	Ticket            string `json:"ticket"`
	ID                string `json:"id"`
	ClientDataJSON    string `json:"client_data_json"`   // base64url
	AttestationObject string `json:"attestation_object"` // base64url
}

// handleEnrollFinish verifies the ceremony against the bound challenge and
// persists the credential under the ticket's role.
func (s *ChatServer) handleEnrollFinish(w http.ResponseWriter, r *http.Request) {
	if s.WebAuthn == nil {
		http.Error(w, "passkey disabled", http.StatusServiceUnavailable)
		return
	}
	if !WAConfig.Enabled {
		http.Error(w, "passkey disabled", http.StatusServiceUnavailable)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req finishRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64*1024)).Decode(&req); err != nil {
		log.Printf("❌ [ENROLL FINISH] bad payload from %s: %v", r.RemoteAddr, err)
		http.Error(w, "bad payload", http.StatusBadRequest)
		return
	}
	if req.Ticket == "" || req.ID == "" || req.ClientDataJSON == "" || req.AttestationObject == "" {
		log.Printf("❌ [ENROLL FINISH] ticket=%s… missing fields (id=%q)", shortCode(req.Ticket), req.ID)
		http.Error(w, "missing fields", http.StatusBadRequest)
		return
	}

	log.Printf("🔐 [ENROLL FINISH] ticket=%s… id=%s… from=%s", shortCode(req.Ticket), shortID(req.ID), r.RemoteAddr)
	_, boundChallenge, err := s.WebAuthn.PendingInfo(req.Ticket)
	if err != nil {
		log.Printf("❌ [ENROLL FINISH] ticket=%s… pending lookup failed: %v", shortCode(req.Ticket), err)
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	log.Printf("🔍 [ENROLL FINISH] ticket=%s… boundChallenge=%s…", shortCode(req.Ticket), boundChallenge[:12])
	parsed, err := parseCreationForImport(req.ClientDataJSON, req.AttestationObject, boundChallenge)
	if err != nil {
		log.Printf("❌ [ENROLL FINISH] ticket=%s… parse failed: %v", shortCode(req.Ticket), err)
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	log.Printf("✅ [ENROLL FINISH] parsed credential_id=%s… counter=%d", shortID(parsed.CredentialID), parsed.Counter)

	err = s.WebAuthn.CompleteEnrollment(req.Ticket, &WAStoredCred{
		CredentialID: parsed.CredentialID,
		PublicKey:    base64.RawURLEncoding.EncodeToString(parsed.PublicKey),
		SignCount:    parsed.Counter,
	})
	if err != nil {
		log.Printf("❌ [ENROLL FINISH] ticket=%s… store failed: %v", shortCode(req.Ticket), err)
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	log.Printf("✅ [ENROLL FINISH] ticket=%s… stored credential_id=%s…", shortCode(req.Ticket), shortID(parsed.CredentialID))
	writeJSON(w, map[string]any{"ok": true, "credential_id": parsed.CredentialID})
}

func shortCode(s string) string {
	if len(s) > 12 {
		return s[:12] + "…"
	}
	return s
}

func shortID(s string) string {
	if len(s) > 12 {
		return s[:12] + "…"
	}
	return s
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

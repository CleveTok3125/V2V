package main

// HTTP endpoints for browser-side passkey enrollment. Access is gated by a
// one-time ticket the admin issued on the server host (see -enroll flag in
// main.go). The ceremony itself runs entirely in the member's browser; this
// server only binds the challenge to the ticket and stores the public half.

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"

	"github.com/CleveTok3125/V2V/internal/guard"
	"github.com/CleveTok3125/V2V/internal/strutil"
	"net/http"
	"time"
)

const enrollBeginCooldown = 30 * time.Second

var enrollBeginCooldowns = guard.NewCooldownMap()

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
	if !enrollBeginCooldowns.Allow(r.RemoteAddr, enrollBeginCooldown) {
		http.Error(w, "too many enroll attempts", http.StatusTooManyRequests)
		return
	}
	s.WebAuthn.PruneExpired()
	logInfof("🔐 [ENROLL BEGIN] ticket=%s… from=%s", strutil.Short(code), r.RemoteAddr)

	challenge, err := randomB64url(32)
	if err != nil {
		logErrorf("❌ [ENROLL BEGIN] ticket=%s… entropy error: %v", strutil.Short(code), err)
		http.Error(w, "entropy error", http.StatusInternalServerError)
		return
	}
	role, err := s.WebAuthn.BindChallenge(code, challenge)
	if err != nil {
		logErrorf("❌ [ENROLL BEGIN] ticket=%s… bind failed: %v", strutil.Short(code), err)
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	logInfof("✅ [ENROLL BEGIN] ticket=%s… role=%s challenge=%s…", strutil.Short(code), role, strutil.Short(challenge))
	userID, _ := randomB64url(16)

	requireResidentKey := true
	writeJSON(w, creationOptions{
		PublicKey: creationOptionsPK{
			Challenge: challenge,
			RP:        rpEnt{ID: WAConfig.RPID, Name: "V2V"},
			User:      userEnt{ID: userID, Name: role + ":" + strutil.ShortN(code, 8)},
			PubKeyCredParams: []algEnt{
				{Type: "public-key", Alg: -7}, // ES256 only
			},
			AuthenticatorSelection: authSel{
				RequireResidentKey: &requireResidentKey,
				ResidentKey:        "required",
				UserVerification:   "required",
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
		logErrorf("❌ [ENROLL FINISH] bad payload from %s: %v", r.RemoteAddr, err)
		http.Error(w, "bad payload", http.StatusBadRequest)
		return
	}
	if req.Ticket == "" || req.ID == "" || req.ClientDataJSON == "" || req.AttestationObject == "" {
		logErrorf("❌ [ENROLL FINISH] ticket=%s… missing fields (id=%q)", strutil.Short(req.Ticket), req.ID)
		http.Error(w, "missing fields", http.StatusBadRequest)
		return
	}

	logInfof("🔐 [ENROLL FINISH] ticket=%s… id=%s… from=%s", strutil.Short(req.Ticket), strutil.Short(req.ID), r.RemoteAddr)
	_, boundChallenge, err := s.WebAuthn.PendingInfo(req.Ticket)
	if err != nil {
		logErrorf("❌ [ENROLL FINISH] ticket=%s… pending lookup failed: %v", strutil.Short(req.Ticket), err)
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	logInfof("🔍 [ENROLL FINISH] ticket=%s… boundChallenge=%s…", strutil.Short(req.Ticket), strutil.Short(boundChallenge))
	created, err := parseCreationLib(req.ClientDataJSON, req.AttestationObject, boundChallenge, req.ID)
	if err != nil {
		logErrorf("❌ [ENROLL FINISH] ticket=%s… parse failed: %v", strutil.Short(req.Ticket), err)
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	logInfof("✅ [ENROLL FINISH] parsed credential_id=%s… counter=%d fmt=%s", strutil.Short(created.CredentialID), created.Counter, created.AttFormat)

	err = s.WebAuthn.CompleteEnrollment(req.Ticket, &WAStoredCred{
		CredentialID:   created.CredentialID,
		PublicKey:      base64.RawURLEncoding.EncodeToString(created.PublicKey),
		SignCount:      created.Counter,
		AttFormat:      created.AttFormat,
		BackupEligible: created.BackupEligible,
		BackupState:    created.BackupState,
	})
	if err != nil {
		logErrorf("❌ [ENROLL FINISH] ticket=%s… store failed: %v", strutil.Short(req.Ticket), err)
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	logInfof("✅ [ENROLL FINISH] ticket=%s… stored credential_id=%s…", strutil.Short(req.Ticket), strutil.Short(created.CredentialID))
	writeJSON(w, map[string]any{"ok": true, "credential_id": created.CredentialID})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

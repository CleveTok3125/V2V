package main

// HTTP endpoints for browser-side passkey enrollment. Access is gated by a
// one-time ticket the admin issued on the server host (see -enroll flag in
// main.go). The ceremony itself runs entirely in the member's browser; this
// server only binds the challenge to the ticket and stores the public half.

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
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

	challenge, err := randomB64url(32)
	if err != nil {
		http.Error(w, "entropy error", http.StatusInternalServerError)
		return
	}
	role, err := s.WebAuthn.BindChallenge(code, challenge)
	if err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	userID, _ := randomB64url(16)

	writeJSON(w, creationOptions{
		PublicKey: creationOptionsPK{
			Challenge: challenge,
			RP:        rpEnt{ID: WAConfig.RPID, Name: "V2V"},
			User:      userEnt{ID: userID, Name: role + ":" + code[:8]},
			PubKeyCredParams: []algEnt{
				{Type: "public-key", Alg: -7},   // ES256
				{Type: "public-key", Alg: -257}, // RS256
			},
			AuthenticatorSelection: authSel{UserVerification: "preferred"},
			Timeout:                int64(enrollChallengeTTL.Seconds() * 1000),
			Attestation:            "none",
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
		http.Error(w, "bad payload", http.StatusBadRequest)
		return
	}
	if req.Ticket == "" || req.ID == "" || req.ClientDataJSON == "" || req.AttestationObject == "" {
		http.Error(w, "missing fields", http.StatusBadRequest)
		return
	}

	_, boundChallenge, err := s.WebAuthn.PendingInfo(req.Ticket)
	if err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	parsed, err := parseCreationForImport(req.ClientDataJSON, req.AttestationObject, boundChallenge)
	if err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}

	err = s.WebAuthn.CompleteEnrollment(req.Ticket, &WAStoredCred{
		CredentialID: parsed.CredentialID,
		PublicKey:    base64.RawURLEncoding.EncodeToString(parsed.PublicKey),
		SignCount:    parsed.Counter,
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	writeJSON(w, map[string]any{"ok": true, "credential_id": parsed.CredentialID})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// runEnrollIssuer is the -enroll mode of the server binary: it issues a
// one-time enrollment ticket for the given role and prints the URL a member
// opens in their browser to run the passkey ceremony. Exits when done.
func runEnrollIssuer(args []string) {
	fs := flag.NewFlagSet("enroll", flag.ContinueOnError)
	role := fs.String("role", "", "role gắn với passkey (bắt buộc)")
	label := fs.String("label", "", "nhãn thiết bị/người (tùy chọn)")
	if err := fs.Parse(args); err != nil {
		os.Exit(1)
	}

	LoadWebauthnEnv()
	if !WAConfig.Enabled {
		fmt.Println("❌ Cần WEBAUTHN_RPID và WEBAUTHN_ORIGIN trong .env")
		os.Exit(1)
	}
	if *role == "" {
		fmt.Println("❌ Thiếu --role")
		os.Exit(1)
	}

	path := os.Getenv("WEBAUTHN_STORE")
	store := NewWebAuthnStore(path)
	code, err := store.CreatePendingTicket(*role, *label, 10*time.Minute)
	if err != nil {
		fmt.Println("❌", err)
		os.Exit(1)
	}
	origin := WAConfig.Origin
	fmt.Println("✅ Ticket đã tạo (hết hạn sau 10 phút, dùng 1 lần).")
	fmt.Printf("Gửi link sau cho người được cấp:\n\n  %s/web/#enroll=%s\n\n", origin, code)
}

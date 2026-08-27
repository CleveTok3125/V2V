package main

// File-backed store for WebAuthn data that the server manages itself:
// pending enrollment tickets and registered credentials per role. Kept
// separate from roles.json (admin-authored) so there is exactly one writer
// per file. All mutations load-modify-rewrite atomically under one mutex.

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const (
	webauthnFileVersion  = 1
	defaultWebauthnStore = "data/webauthn.json"
)

var (
	errTicketUnknown = errors.New("ticket không tồn tại hoặc đã hết hạn")
	errTicketUsed    = errors.New("ticket đã được sử dụng")
)

type WAStoredCred struct {
	CredentialID string `json:"credential_id"`
	PublicKey    string `json:"public_key"` // COSE_Key CBOR, base64url
	SignCount    uint32 `json:"sign_count"`
	Label        string `json:"label,omitempty"`
	AddedAt      string `json:"added_at,omitempty"`
}

type WAPending struct {
	Code      string    `json:"code"`
	Role      string    `json:"role"`
	Label     string    `json:"label,omitempty"`
	ExpiresAt time.Time `json:"expires_at"`
	Challenge string    `json:"challenge,omitempty"` // legacy b64url, kept for compat
	// SessionData holds the webauthn.SessionData JSON for 100% library-based
	// registration (challenge, userID, etc.).
	SessionData []byte `json:"session_data,omitempty"`
	Used      bool   `json:"used"`
}

type webauthnFile struct {
	Version     int                        `json:"version"`
	Pending     []*WAPending               `json:"pending,omitempty"`
	Credentials map[string][]*WAStoredCred `json:"credentials,omitempty"`
}

// WebAuthnStore persists to WEBAUTHN_STORE (default data/webauthn.json).
type WebAuthnStore struct {
	mu   sync.Mutex
	path string
}

func NewWebAuthnStore(path string) *WebAuthnStore {
	if path == "" {
		path = defaultWebauthnStore
	}
	return &WebAuthnStore{path: path}
}

func (s *WebAuthnStore) loadFile() (*webauthnFile, error) {
	f := &webauthnFile{
		Version:     webauthnFileVersion,
		Credentials: map[string][]*WAStoredCred{},
	}
	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return f, nil
		}
		return nil, err
	}
	if len(data) == 0 {
		return f, nil
	}
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("webauthn store hỏng (%w)", err)
	}
	if f.Credentials == nil {
		f.Credentials = map[string][]*WAStoredCred{}
	}
	return f, nil
}

func (s *WebAuthnStore) saveFile(f *webauthnFile) error {
	if dir := filepath.Dir(s.path); dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
	}
	out, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, out, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func (s *WebAuthnStore) mutate(fn func(f *webauthnFile) error) error {
	if s == nil {
		return errors.New("webauthn store not initialized")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := s.loadFile()
	if err != nil {
		return err
	}
	if err := fn(f); err != nil {
		return err
	}
	return s.saveFile(f)
}

func (s *WebAuthnStore) view(fn func(f *webauthnFile) error) error {
	if s == nil {
		return errors.New("webauthn store not initialized")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := s.loadFile()
	if err != nil {
		return err
	}
	return fn(f)
}

var errPendingNotFound = errors.New("pending ticket not found")

func findPending(f *webauthnFile, code string) (*WAPending, error) {
	for _, p := range f.Pending {
		if p.Code == code {
			switch {
			case p.Used:
				return nil, errTicketUsed
			case time.Now().After(p.ExpiresAt):
				return nil, errTicketUnknown
			}
			return p, nil
		}
	}
	return nil, errTicketUnknown
}

// CreatePendingTicket registers a one-time enrollment ticket.
func (s *WebAuthnStore) CreatePendingTicket(role, label string, ttl time.Duration) (string, error) {
	if role == "" {
		return "", errors.New("enroll: thiếu --role")
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	code := hex.EncodeToString(b)
	err := s.mutate(func(f *webauthnFile) error {
		f.Pending = append(f.Pending, &WAPending{
			Code:      code,
			Role:      role,
			Label:     label,
			ExpiresAt: time.Now().Add(ttl),
		})
		return nil
	})
	if err == nil {
		log.Printf("🎫 [TICKET CREATED] code=%s… role=%s label=%q expires=%s", code[:12], role, label, time.Now().Add(ttl).Format(time.RFC3339))
	} else {
		log.Printf("❌ [TICKET CREATE FAILED] role=%s: %v", role, err)
	}
	return code, err
}

// BindChallenge validates the ticket for ceremony start and binds a fresh
// challenge (returned as base64url) to it. Kept for manual challenge flow
// (e.g., synthetic tests) and for backward compat.
func (s *WebAuthnStore) BindChallenge(code, challengeB64 string) (role string, err error) {
	err = s.mutate(func(f *webauthnFile) error {
		p, perr := findPending(f, code)
		if perr != nil {
			return perr
		}
		p.Challenge = challengeB64
		role = p.Role
		return nil
	})
	if err != nil {
		log.Printf("❌ [TICKET BIND] code=%s… failed: %v", shortTicket(code), err)
	} else {
		log.Printf("🔗 [TICKET BIND] code=%s… role=%s challenge=%s…", shortTicket(code), role, challengeB64[:12])
	}
	return role, err
}

// BindSessionData stores the full webauthn.SessionData for a ticket. Used
// by the 100% library registration flow where the session contains more than
// just the challenge (userID, etc.).
func (s *WebAuthnStore) BindSessionData(code string, sessionData []byte, challengeB64 string) (role string, err error) {
	err = s.mutate(func(f *webauthnFile) error {
		p, perr := findPending(f, code)
		if perr != nil {
			return perr
		}
		p.Challenge = challengeB64
		p.SessionData = sessionData
		role = p.Role
		return nil
	})
	if err != nil {
		log.Printf("❌ [TICKET BIND SESSION] code=%s… failed: %v", shortTicket(code), err)
	} else {
		log.Printf("🔗 [TICKET BIND SESSION] code=%s… role=%s", shortTicket(code), role)
	}
	return role, err
}

// SessionData returns the stored webauthn.SessionData for a ticket.
func (s *WebAuthnStore) SessionData(code string) ([]byte, error) {
	var data []byte
	err := s.view(func(f *webauthnFile) error {
		p, perr := findPending(f, code)
		if perr != nil {
			return perr
		}
		if len(p.SessionData) == 0 {
			return errors.New("no session data for ticket")
		}
		data = append([]byte(nil), p.SessionData...)
		return nil
	})
	return data, err
}

func shortTicket(s string) string {
	if len(s) > 12 {
		return s[:12] + "…"
	}
	return s
}

// CompleteEnrollment stores the credential under the ticket's role and marks
// the ticket used. Ceremony validation (challenge/origin/signature) is the
// caller's job — this only handles bookkeeping.
func (s *WebAuthnStore) CompleteEnrollment(code string, cred *WAStoredCred) error {
	err := s.mutate(func(f *webauthnFile) error {
		pending, err := findPending(f, code)
		if err != nil {
			return err
		}
		cred.Label = pending.Label
		list := f.Credentials[pending.Role]
		for _, ex := range list {
			if ex.CredentialID == cred.CredentialID {
				return errors.New("credential đã tồn tại")
			}
		}
		f.Credentials[pending.Role] = append(list, cred)
		pending.Used = true
		return nil
	})
	if err != nil {
		log.Printf("❌ [ENROLL STORE] CompleteEnrollment code=%s… failed: %v", shortTicket(code), err)
	} else {
		log.Printf("✅ [ENROLL STORE] CompleteEnrollment code=%s… credential_id=%s… stored", shortTicket(code), shortTicket(cred.CredentialID))
	}
	return err
}

// PendingInfo returns the role and bound challenge of a valid, unused ticket.
func (s *WebAuthnStore) PendingInfo(code string) (role, challengeB64 string, err error) {
	err = s.view(func(f *webauthnFile) error {
		p, perr := findPending(f, code)
		if perr != nil {
			return perr
		}
		role = p.Role
		challengeB64 = p.Challenge
		return nil
	})
	return role, challengeB64, err
}

// Credential returns a stored credential for role/credentialID lookups.
func (s *WebAuthnStore) Credential(role, credentialID string) (*WAStoredCred, bool) {
	var out *WAStoredCred
	err := s.view(func(f *webauthnFile) error {
		for _, c := range f.Credentials[role] {
			if c.CredentialID == credentialID {
				out = c
				return nil
			}
		}
		return nil
	})
	if err != nil {
		return nil, false
	}
	cp := *out
	return &cp, out != nil
}

// UpdateSignCount persists an increased sign counter; refuses decreases.
func (s *WebAuthnStore) UpdateSignCount(role, credentialID string, count uint32) error {
	return s.mutate(func(f *webauthnFile) error {
		for _, c := range f.Credentials[role] {
			if c.CredentialID == credentialID {
				if count <= c.SignCount {
					return errors.New("sign counter không tăng")
				}
				c.SignCount = count
				return nil
			}
		}
		return os.ErrNotExist
	})
}

// PruneExpired removes stale/used pending tickets; called opportunistically.
func (s *WebAuthnStore) PruneExpired() {
	if s == nil {
		return
	}
	_ = s.mutate(func(f *webauthnFile) error {
		now := time.Now()
		kept := f.Pending[:0]
		for _, p := range f.Pending {
			if p.Used || now.After(p.ExpiresAt) {
				continue
			}
			kept = append(kept, p)
		}
		f.Pending = kept
		return nil
	})
}

package main

import (
	"fmt"
	"os"
	"time"
	"encoding/json"

	"github.com/CleveTok3125/V2V/internal/identity"
	"github.com/CleveTok3125/V2V/internal/strutil"
)

type waPending struct {
	Code      string    `json:"code"`
	Role      string    `json:"role"`
	Label     string    `json:"label,omitempty"`
	ExpiresAt time.Time `json:"expires_at"`
	Challenge string    `json:"challenge,omitempty"`
	Used      bool      `json:"used"`
}

// waStoreFile mirrors the server-managed store schema (subset).
type waStoreFile struct {
	Version     int                         `json:"version"`
	Pending     []waPending                 `json:"pending,omitempty"`
	Credentials map[string][]map[string]any `json:"credentials,omitempty"`
}

func loadStore(path string) (*waStoreFile, error) {
	f := &waStoreFile{Version: 1, Credentials: map[string][]map[string]any{}}
	data, err := os.ReadFile(path)
	switch {
	case os.IsNotExist(err):
		return f, nil
	case err != nil:
		return nil, err
	}
	if len(data) == 0 {
		return f, nil
	}
	if err := json.Unmarshal(data, f); err != nil {
		return nil, fmt.Errorf("store hỏng (%w) — không tự ghi đè", err)
	}
	if f.Credentials == nil {
		f.Credentials = map[string][]map[string]any{}
	}
	return f, nil
}

func str(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprint(v)
}


type ListCmd struct {
	Store string `help:"Đường dẫn store" default:"data/webauthn.json" env:"WEBAUTHN_STORE"`
}

func saveStore(path string, f *waStoreFile) error {
	out, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	return identity.AtomicWriteFile(path, out, 0o600)
}

func (l *ListCmd) Run() error {
	f, err := loadStore(l.Store)
	if err != nil {
		return err
	}
	now := time.Now()
	fmt.Println("== Pending tickets ==")
	for _, p := range f.Pending {
		state := "ACTIVE"
		switch {
		case p.Used:
			state = "USED"
		case now.After(p.ExpiresAt):
			state = "EXPIRED"
		}
		fmt.Printf("  [%s] role=%s label=%q expires=%s code=%s…\n",
			state, p.Role, p.Label, p.ExpiresAt.Format(time.RFC3339), strutil.Short(p.Code))
	}
	fmt.Println("== Credentials ==")
	for role, creds := range f.Credentials {
		for _, c := range creds {
			fmt.Printf("  role=%s cred=%s… added=%v\n", role, strutil.Short(str(c["credential_id"])), str(c["added_at"]))
		}
	}
	return nil
}

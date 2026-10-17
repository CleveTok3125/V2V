package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"charm.land/huh/v2"
	"github.com/CleveTok3125/V2V/internal/tui"
	"github.com/CleveTok3125/V2V/lib/env"
)

type EnrollCmd struct {
	Role  string        `help:"Role gắn với passkey" default:"member" short:"r"`
	Label string        `help:"Nhãn thiết bị/người" short:"l"`
	Store string        `help:"Đường dẫn store" env:"WEBAUTHN_STORE" short:"s"`
	TTL   time.Duration `help:"Thời gian hiệu lực ticket" default:"10m"`
}

func (e *EnrollCmd) Run() error {
	if strings.TrimSpace(e.Store) == "" {
		e.Store = defaultWebauthnStore()
	} else {
		e.Store = resolveInstancePath(e.Store)
	}
	if tui.HasControllingTTY() {
		form := huh.NewForm(huh.NewGroup(
			huh.NewInput().Title("Role").Value(&e.Role).Validate(nonEmpty),
			huh.NewInput().Title("Nhãn thiết bị/người (tùy chọn)").Value(&e.Label),
		))
		if err := form.Run(); err != nil {
			return err
		}
	}
	// Check role exists, warn if not (role should be created via `v2vctl role create`)
	if data, err := os.ReadFile(rolesPath()); err == nil && len(data) > 0 {
		var root map[string]any
		_ = json.Unmarshal(data, &root)
		if _, ok := root[e.Role]; !ok {
			fmt.Printf("⚠️  Role \"%s\" chưa tồn tại trong roles.json — hãy tạo trước bằng `v2vctl role create --role %s`\n", e.Role, e.Role)
		}
	}

	f, err := loadStore(e.Store)
	if err != nil {
		return err
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return err
	}
	// Resolved before the ticket is written: a ticket whose link cannot be
	// used is worse than no ticket, because it occupies the store and looks
	// like a working invitation.
	origin := enrollOrigin()
	if origin == "" {
		return fmt.Errorf("WEBAUTHN_ORIGIN chưa đặt (biến môi trường hoặc %s) — không tạo được link enroll", filepath.Join(v2vctlRoot, ".env"))
	}

	code := hex.EncodeToString(b)
	f.Pending = append(f.Pending, waPending{
		Code:      code,
		Role:      e.Role,
		Label:     e.Label,
		ExpiresAt: time.Now().Add(e.TTL),
	})
	if err := saveStore(e.Store, f); err != nil {
		return err
	}
	fmt.Println("✅ Ticket đã tạo (single-use, TTL " + e.TTL.String() + ").")
	fmt.Printf("Gửi link sau cho người được cấp:\n\n  %s/web/#enroll=%s\n\n", origin, code)
	return nil
}

// enrollOrigin resolves the origin enrollment links point at.
//
// Empty means no link can be built, so the caller must refuse
// rather than print a bare path.
func enrollOrigin() string {
	if v := strings.TrimSpace(env.WebauthnOrigin()); v != "" {
		return strings.TrimRight(v, "/")
	}
	data, err := os.ReadFile(filepath.Join(v2vctlRoot, ".env"))
	if err != nil {
		return ""
	}
	return strings.TrimRight(activeEnvValue(data, "WEBAUTHN_ORIGIN"), "/")
}

// activeEnvValue reads one key from an .env document, ignoring commented
// lines: the shipped template has every key commented out, and treating one
// as set would advertise an origin nobody configured.
func activeEnvValue(data []byte, key string) string {
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		name, value, found := strings.Cut(line, "=")
		if !found || strings.TrimSpace(name) != key {
			continue
		}
		return strings.Trim(strings.TrimSpace(value), `"'`)
	}
	return ""
}

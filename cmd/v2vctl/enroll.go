package main

import (
	"fmt"
	"os"
	"time"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"

	"github.com/CleveTok3125/V2V/internal/env"
	"github.com/CleveTok3125/V2V/internal/tui"
	"github.com/charmbracelet/huh"
)

type EnrollCmd struct {
	Role  string        `help:"Role gắn với passkey" default:"member"`
	Label string        `help:"Nhãn thiết bị/người"`
	Store string        `help:"Đường dẫn store" default:"data/webauthn.json" env:"WEBAUTHN_STORE"`
	TTL   time.Duration `help:"Thời gian hiệu lực ticket" default:"10m"`
}

func (e *EnrollCmd) Run() error {
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
	origin := env.WebauthnOrigin()
	fmt.Println("✅ Ticket đã tạo (single-use, TTL " + e.TTL.String() + ").")
	fmt.Printf("Gửi link sau cho người được cấp:\n\n  %s/web/#enroll=%s\n\n", origin, code)
	return nil
}

// --- migrate ----------------------------------------------------------------


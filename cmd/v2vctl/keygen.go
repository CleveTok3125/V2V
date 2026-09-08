package main

import (
	"errors"
	"fmt"
	"os"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"

	"github.com/CleveTok3125/V2V/identity"
	"github.com/CleveTok3125/V2V/internal/tui"
	"github.com/charmbracelet/huh"
)

type Ed25519Keygen struct {
	Role         string `help:"Role gắn với danh tính" default:"admin"`
	Out          string `help:"Nơi ghi container" default:"key.json"`
	ServerPubKey string `help:"Server public key hex (chống phishing, thay thế host pin)"`
}

type PasskeyKeygen struct {
	Role   string `help:"Role gắn với passkey" default:"member"`
	Out    string `help:"Nơi ghi container" default:"key.json"`
	RPID   string `help:"RP ID; fallback env WEBAUTHN_RPID"`
	Origin string `help:"Origin; fallback env WEBAUTHN_ORIGIN"`
	Label  string `help:"Nhãn thiết bị/người"`
}

func (c *Ed25519Keygen) Run() error {
	if tui.HasControllingTTY() {
		if c.ServerPubKey == "" {
			if data, err := os.ReadFile("data/server_identity.json"); err == nil {
				var sid map[string]any
				if json.Unmarshal(data, &sid) == nil {
					if pub, ok := sid["public_key"].(string); ok {
						c.ServerPubKey = pub
					}
				}
			}
		}
		form := huh.NewForm(huh.NewGroup(
			huh.NewInput().Title("Role").Value(&c.Role).Validate(nonEmpty),
			huh.NewInput().Title("Nơi lưu key.json").Value(&c.Out),
			huh.NewInput().Title("Server public key (hex, Enter = không pin)").Value(&c.ServerPubKey),
		))
		if err := form.Run(); err != nil {
			return err
		}
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	shield := make([]byte, 16)
	if _, err := rand.Read(shield); err != nil {
		return err
	}

	idf, err := loadContainer(c.Out)
	if err != nil {
		return err
	}
	idf.Ed25519 = &identity.Ed25519Identity{
		Role:         c.Role,
		PrivateKey:   hex.EncodeToString(priv),
		HmacShield:   hex.EncodeToString(shield),
		ServerPubKey: c.ServerPubKey,
	}
	if err := saveContainer(idf, c.Out); err != nil {
		return err
	}
	fmt.Printf("\n💾 Đã lưu khóa bí mật tại %s (chmod 600)\n", c.Out)

	// Output snippet for role add-identity (paste mode)
	snippet := map[string]any{
		"public_key":    hex.EncodeToString(pub),
		"hmac_shield":   hex.EncodeToString(shield),
		"server_pubkey": c.ServerPubKey,
	}
	out, _ := json.MarshalIndent(snippet, "", "  ")
	fmt.Print("📤 Dùng lệnh sau để thêm vào role (hỗ trợ paste):\n\n")
	fmt.Printf("  v2vctl role add-identity %s --public-key %s --hmac-shield %s", c.Role, hex.EncodeToString(pub), hex.EncodeToString(shield))
	if c.ServerPubKey != "" {
		fmt.Printf(" --server-pubkey %s", c.ServerPubKey)
	}
	fmt.Println()
	fmt.Println("\nHoặc paste JSON:\n", string(out))
	return nil
}

// --- keygen passkey (soft) -----------------------------------------------

func (c *PasskeyKeygen) Run() error {
	if tui.HasControllingTTY() {
		if c.RPID == "" {
			c.RPID = os.Getenv("WEBAUTHN_RPID")
		}
		if c.Origin == "" {
			c.Origin = os.Getenv("WEBAUTHN_ORIGIN")
		}
		form := huh.NewForm(huh.NewGroup(
			huh.NewInput().Title("Role").Value(&c.Role).Validate(nonEmpty),
			huh.NewInput().Title("RP ID").Value(&c.RPID).Validate(nonEmpty),
			huh.NewInput().Title("Origin").Value(&c.Origin).Validate(nonEmpty),
			huh.NewInput().Title("Nhãn thiết bị/người (tùy chọn)").Value(&c.Label),
			huh.NewInput().Title("Nơi lưu key.json").Value(&c.Out),
		))
		if err := form.Run(); err != nil {
			return err
		}
	}
	rpid := c.RPID
	if rpid == "" {
		rpid = os.Getenv("WEBAUTHN_RPID")
	}
	origin := c.Origin
	if origin == "" {
		origin = os.Getenv("WEBAUTHN_ORIGIN")
	}
	if rpid == "" || origin == "" {
		return errors.New("passkey cần --rpid và --origin (hoặc env WEBAUTHN_RPID/WEBAUTHN_ORIGIN)")
	}
	pk, err := identity.GeneratePasskey(c.Role, rpid, origin)
	if err != nil {
		return err
	}

	idf, err := loadContainer(c.Out)
	if err != nil {
		return err
	}
	idf.Passkey = pk
	if err := saveContainer(idf, c.Out); err != nil {
		return err
	}
	fmt.Printf("\n💾 Đã lưu khóa bí mật tại %s (chmod 600)\n", c.Out)

	snippet, err := pk.RolesSnippet()
	if err != nil {
		return err
	}
	fmt.Print("📤 Dùng lệnh sau để thêm vào role (hỗ trợ paste):\n\n")
	fmt.Printf("  v2vctl role add-passkey %s --credential-id %s --public-key %s\n", c.Role, pk.CredentialID, pk.PublicKey)
	fmt.Println("\nHoặc paste JSON:\n", snippet)
	return nil
}

// --- enroll ---------------------------------------------------------------


package main

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
	"encoding/json"

	"github.com/CleveTok3125/V2V/identity"
	"github.com/CleveTok3125/V2V/internal/tui"
	"github.com/charmbracelet/huh"
)

type RoleCmd struct {
	Create     RoleCreateCmd     `cmd:"" help:"Tạo role mới (báo ghi đè nếu đã tồn tại)"`
	List       RoleListCmd       `cmd:"" help:"Liệt kê roles"`
	Show       RoleShowCmd       `cmd:"" help:"Hiển thị cấu hình role"`
	Update     RoleUpdateCmd     `cmd:"" help:"Cập nhật prefix/quyền của role"`
	Delete     RoleDeleteCmd     `cmd:"" help:"Xóa role"`
	AddIdentity RoleAddIdentityCmd `cmd:"" name:"add-identity" help:"Thêm identity ed25519 vào role"`
	AddPasskey  RoleAddPasskeyCmd  `cmd:"" name:"add-passkey" help:"Thêm passkey vào role"`
	Import     RoleImportCmd     `cmd:"" help:"Import roles từ file hoặc paste JSON"`
}

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

type RoleCreateCmd struct {
	Role      string `arg:"" optional:"" help:"Tên role"`
	Prefix    string `help:"Prefix hiển thị" default:"[Member] "`
	Unlimited bool   `help:"Quyền chat không giới hạn"`
	Force     bool   `help:"Ghi đè nếu role đã tồn tại"`
}

type RoleListCmd struct{}

type RoleShowCmd struct {
	Role string `arg:"" help:"Tên role"`
}

type RoleUpdateCmd struct {
	Role      string `arg:"" help:"Tên role"`
	Prefix    string `help:"Prefix hiển thị"`
	Unlimited *bool  `help:"Quyền chat không giới hạn (true/false)"`
	Force     bool   `help:"Ghi đè"`
}

type RoleDeleteCmd struct {
	Role  string `arg:"" help:"Tên role"`
	Force bool   `help:"Không hỏi xác nhận"`
}

type RoleAddIdentityCmd struct {
	Role         string `arg:"" help:"Tên role"`
	PublicKey    string `help:"Public key hex (64 chars)"`
	HmacShield   string `help:"HMAC shield hex (32 chars)"`
	ServerPubKey string `help:"Server public key hex"`
	Paste        bool   `help:"Đọc JSON snippet từ stdin (paste)"`
	File         string `help:"Đọc JSON từ file"`
	Force        bool   `help:"Ghi đè nếu identity đã tồn tại"`
}

type RoleAddPasskeyCmd struct {
	Role         string `arg:"" help:"Tên role"`
	CredentialID string `help:"Credential ID base64url"`
	PublicKey    string `help:"Public key COSE base64url"`
	Paste        bool   `help:"Đọc JSON snippet từ stdin (paste)"`
	File         string `help:"Đọc JSON từ file"`
}

type RoleImportCmd struct {
	File  string `help:"File JSON roles để import" short:"f"`
	Paste bool   `help:"Đọc JSON từ stdin (paste)"`
	Force bool   `help:"Ghi đè roles đã tồn tại"`
}

func loadRolesMap() (map[string]any, error) {
	data, err := os.ReadFile(rolesPath())
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]any{}, nil
		}
		return nil, err
	}
	if len(data) == 0 {
		return map[string]any{}, nil
	}
	var root map[string]any
	if err := json.Unmarshal(data, &root); err != nil {
		return nil, fmt.Errorf("roles.json không hợp lệ: %w", err)
	}
	if root == nil {
		root = map[string]any{}
	}
	return root, nil
}

func readPasteJSON() ([]byte, error) {
	// Try file first if provided via --file handled separately
	// For --paste, read from stdin; if interactive and no pipe, prompt via huh
	if tui.HasControllingTTY() {
		var pasted string
		form := huh.NewForm(huh.NewGroup(
			huh.NewText().Title("Paste JSON snippet").Value(&pasted).Validate(nonEmpty),
		))
		if err := form.Run(); err != nil {
			return nil, err
		}
		return []byte(pasted), nil
	}
	// Non-interactive: read all stdin
	data, err := os.ReadFile("/dev/stdin")
	if err != nil {
		// Fallback to os.Stdin read
		var buf strings.Builder
		tmp := make([]byte, 4096)
		for {
			n, rerr := os.Stdin.Read(tmp)
			if n > 0 {
				buf.Write(tmp[:n])
			}
			if rerr != nil {
				break
			}
		}
		data = []byte(buf.String())
	}
	if len(data) == 0 {
		return nil, errors.New("không có dữ liệu paste")
	}
	return data, nil
}

func (c *RoleCreateCmd) Run() error {
	if tui.HasControllingTTY() && c.Role == "" {
		form := huh.NewForm(huh.NewGroup(
			huh.NewInput().Title("Tên role").Value(&c.Role).Validate(nonEmpty),
			huh.NewInput().Title("Prefix hiển thị").Value(&c.Prefix),
			huh.NewConfirm().Title("Quyền chat không giới hạn?").Value(&c.Unlimited),
		))
		if err := form.Run(); err != nil {
			return err
		}
	}
	if strings.TrimSpace(c.Role) == "" {
		return errors.New("role là bắt buộc")
	}
	root, err := loadRolesMap()
	if err != nil {
		return err
	}
	_, exists := root[c.Role]
	if exists && !c.Force {
		if tui.HasControllingTTY() {
			var overwrite bool
			form := huh.NewForm(huh.NewGroup(
				huh.NewConfirm().Title(fmt.Sprintf("Role \"%s\" đã tồn tại — ghi đè?", c.Role)).Affirmative("Ghi đè").Negative("Hủy").Value(&overwrite),
			))
			if err := form.Run(); err != nil {
				return err
			}
			if !overwrite {
				return errors.New("hủy tạo role")
			}
		} else {
			return fmt.Errorf("role \"%s\" đã tồn tại, dùng --force để ghi đè", c.Role)
		}
	}
	if err := identity.MergeRolesFile(rolesPath(), c.Role, func(e map[string]any) {
		e["can_message_unlimited"] = c.Unlimited
		e["custom_prefix"] = c.Prefix
		if _, ok := e["identities"]; !ok {
			e["identities"] = []map[string]string{}
		}
		if _, ok := e["passkeys"]; !ok {
			e["passkeys"] = []any{}
		}
	}); err != nil {
		return err
	}
	if exists {
		fmt.Printf("⚠️  Đã ghi đè role \"%s\" trong roles.json\n", c.Role)
	} else {
		fmt.Printf("✅ Đã tạo role \"%s\" trong roles.json\n", c.Role)
	}
	return nil
}

func (c *RoleListCmd) Run() error {
	root, err := loadRolesMap()
	if err != nil {
		return err
	}
	if len(root) == 0 {
		fmt.Println("Chưa có role nào trong roles.json")
		return nil
	}
	fmt.Printf("%-20s %-10s %s\n", "ROLE", "UNLIMITED", "PREFIX")
	fmt.Println(strings.Repeat("-", 50))
	for role, v := range root {
		m, _ := v.(map[string]any)
		unlimited, _ := m["can_message_unlimited"].(bool)
		prefix, _ := m["custom_prefix"].(string)
		// Count identities/passkeys
		var idCount, pkCount int
		if arr, ok := m["identities"].([]any); ok {
			idCount = len(arr)
		}
		if arr, ok := m["passkeys"].([]any); ok {
			pkCount = len(arr)
		}
		fmt.Printf("%-20s %-10v %q (id:%d pk:%d)\n", role, unlimited, prefix, idCount, pkCount)
	}
	return nil
}

func (c *RoleShowCmd) Run() error {
	root, err := loadRolesMap()
	if err != nil {
		return err
	}
	v, ok := root[c.Role]
	if !ok {
		return fmt.Errorf("role \"%s\" không tồn tại", c.Role)
	}
	out, _ := json.MarshalIndent(map[string]any{c.Role: v}, "", "  ")
	fmt.Println(string(out))
	return nil
}

func (c *RoleUpdateCmd) Run() error {
	if strings.TrimSpace(c.Role) == "" {
		return errors.New("role là bắt buộc")
	}
	root, err := loadRolesMap()
	if err != nil {
		return err
	}
	entry, ok := root[c.Role]
	if !ok {
		return fmt.Errorf("role \"%s\" không tồn tại, dùng `role create` trước", c.Role)
	}
	// Interactive prefill if needed
	if tui.HasControllingTTY() {
		m, _ := entry.(map[string]any)
		curPrefix, _ := m["custom_prefix"].(string)
		curUnlimited, _ := m["can_message_unlimited"].(bool)
		if c.Prefix == "" && c.Unlimited == nil {
			// Prompt both
			var newPrefix = curPrefix
			var newUnlimited = curUnlimited
			form := huh.NewForm(huh.NewGroup(
				huh.NewInput().Title("Prefix hiển thị").Value(&newPrefix),
				huh.NewConfirm().Title("Quyền chat không giới hạn?").Value(&newUnlimited),
			))
			if err := form.Run(); err != nil {
				return err
			}
			c.Prefix = newPrefix
			c.Unlimited = &newUnlimited
		} else if c.Prefix == "" {
			// Only unlimited provided via flag, keep prefix
			c.Prefix = curPrefix
		} else if c.Unlimited == nil {
			// Only prefix provided
			b := curUnlimited
			c.Unlimited = &b
		}
	}
	// For non-interactive, if Prefix not set and Unlimited nil, keep existing
	m, _ := entry.(map[string]any)
	if c.Prefix == "" {
		if cur, ok := m["custom_prefix"].(string); ok {
			c.Prefix = cur
		}
	}
	return identity.MergeRolesFile(rolesPath(), c.Role, func(e map[string]any) {
		if c.Prefix != "" || m["custom_prefix"] == nil {
			e["custom_prefix"] = c.Prefix
		}
		if c.Unlimited != nil {
			e["can_message_unlimited"] = *c.Unlimited
		} else if _, ok := e["can_message_unlimited"]; !ok {
			e["can_message_unlimited"] = false
		}
	})
}

func (c *RoleDeleteCmd) Run() error {
	root, err := loadRolesMap()
	if err != nil {
		return err
	}
	if _, ok := root[c.Role]; !ok {
		return fmt.Errorf("role \"%s\" không tồn tại", c.Role)
	}
	if !c.Force && tui.HasControllingTTY() {
		var confirm bool
		form := huh.NewForm(huh.NewGroup(
			huh.NewConfirm().Title(fmt.Sprintf("Xóa role \"%s\"?", c.Role)).Affirmative("Xóa").Negative("Hủy").Value(&confirm),
		))
		if err := form.Run(); err != nil {
			return err
		}
		if !confirm {
			return errors.New("hủy xóa role")
		}
	} else if !c.Force {
		return fmt.Errorf("role \"%s\" tồn tại, dùng --force để xóa", c.Role)
	}
	delete(root, c.Role)
	data, _ := json.MarshalIndent(root, "", "  ")
	if err := identity.AtomicWriteFile(rolesPath(), data, 0o600); err != nil {
		return err
	}
	fmt.Printf("✅ Đã xóa role \"%s\"\n", c.Role)
	return nil
}

func (c *RoleAddIdentityCmd) Run() error {
	if tui.HasControllingTTY() && c.Role == "" {
		form := huh.NewForm(huh.NewGroup(
			huh.NewInput().Title("Role").Value(&c.Role).Validate(nonEmpty),
		))
		if err := form.Run(); err != nil {
			return err
		}
	}
	if strings.TrimSpace(c.Role) == "" {
		return errors.New("role là bắt buộc")
	}
	// Handle paste/file
	if c.Paste || c.File != "" {
		var data []byte
		var err error
		if c.File != "" {
			data, err = os.ReadFile(c.File)
			if err != nil {
				return err
			}
		} else {
			data, err = readPasteJSON()
			if err != nil {
				return err
			}
		}
		// Try to parse as snippet: could be {"public_key":..., "hmac_shield":...} or full role map or identities array
		var raw map[string]any
		if err := json.Unmarshal(data, &raw); err != nil {
			return fmt.Errorf("JSON không hợp lệ: %w", err)
		}
		// Detect shape: if contains public_key directly, it's single identity
		if pk, ok := raw["public_key"].(string); ok {
			c.PublicKey = pk
			if hs, ok := raw["hmac_shield"].(string); ok {
				c.HmacShield = hs
			}
			if sp, ok := raw["server_pubkey"].(string); ok {
				c.ServerPubKey = sp
			}
		} else {
			// Try full roles.json or role wrapper: look for identities[0]
			for _, v := range raw {
				if m, ok := v.(map[string]any); ok {
					if ids, ok := m["identities"].([]any); ok && len(ids) > 0 {
						if id0, ok := ids[0].(map[string]any); ok {
							c.PublicKey, _ = id0["public_key"].(string)
							c.HmacShield, _ = id0["hmac_shield"].(string)
							c.ServerPubKey, _ = id0["server_pubkey"].(string)
							break
						}
					}
					// Direct identities map
					if _, ok := m["public_key"]; ok {
						c.PublicKey, _ = m["public_key"].(string)
						c.HmacShield, _ = m["hmac_shield"].(string)
						c.ServerPubKey, _ = m["server_pubkey"].(string)
					}
				}
			}
		}
	}
	// Interactive prompt for missing fields if still empty and TTY
	if tui.HasControllingTTY() {
		if c.PublicKey == "" || c.HmacShield == "" {
			form := huh.NewForm(huh.NewGroup(
				huh.NewInput().Title("Public key hex").Value(&c.PublicKey).Validate(nonEmpty),
				huh.NewInput().Title("HMAC shield hex").Value(&c.HmacShield).Validate(nonEmpty),
				huh.NewInput().Title("Server pubkey hex (Enter = không pin)").Value(&c.ServerPubKey),
			))
			if err := form.Run(); err != nil {
				return err
			}
		}
	}
	if c.PublicKey == "" || c.HmacShield == "" {
		return errors.New("public_key và hmac_shield là bắt buộc (hoặc dùng --paste)")
	}
	// Check role exists, warn if not
	root, _ := loadRolesMap()
	_, exists := root[c.Role]
	if !exists {
		fmt.Printf("⚠️  Role \"%s\" chưa tồn tại — sẽ tạo mới\n", c.Role)
	}
	// Append identity, dedupe by public_key
	return identity.MergeRolesFile(rolesPath(), c.Role, func(e map[string]any) {
		list, _ := e["identities"].([]any)
		newEntry := map[string]any{
			"public_key":    c.PublicKey,
			"hmac_shield":   c.HmacShield,
			"server_pubkey": c.ServerPubKey,
		}
		for i, raw := range list {
			if m, ok := raw.(map[string]any); ok && m["public_key"] == c.PublicKey {
				if c.Force {
					list[i] = newEntry
					e["identities"] = list
					fmt.Printf("⚠️  Đã ghi đè identity %s trong role \"%s\"\n", c.PublicKey[:8], c.Role)
				} else {
					fmt.Printf("⚠️  Identity %s đã tồn tại trong role \"%s\" (dùng --force để ghi đè)\n", c.PublicKey[:8], c.Role)
				}
				return
			}
		}
		e["identities"] = append(list, newEntry)
		if _, ok := e["passkeys"]; !ok {
			e["passkeys"] = []any{}
		}
		if _, ok := e["can_message_unlimited"]; !ok {
			e["can_message_unlimited"] = false
		}
		if _, ok := e["custom_prefix"]; !ok {
			e["custom_prefix"] = ""
		}
		fmt.Printf("✅ Đã thêm identity vào role \"%s\"\n", c.Role)
	})
}

func (c *RoleAddPasskeyCmd) Run() error {
	if tui.HasControllingTTY() && c.Role == "" {
		form := huh.NewForm(huh.NewGroup(
			huh.NewInput().Title("Role").Value(&c.Role).Validate(nonEmpty),
		))
		if err := form.Run(); err != nil {
			return err
		}
	}
	if strings.TrimSpace(c.Role) == "" {
		return errors.New("role là bắt buộc")
	}
	if c.Paste || c.File != "" {
		var data []byte
		var err error
		if c.File != "" {
			data, err = os.ReadFile(c.File)
			if err != nil {
				return err
			}
		} else {
			data, err = readPasteJSON()
			if err != nil {
				return err
			}
		}
		// Strip // comment lines from RolesSnippet()
		lines := strings.Split(string(data), "\n")
		var cleaned []string
		for _, l := range lines {
			if strings.HasPrefix(strings.TrimSpace(l), "//") {
				continue
			}
			cleaned = append(cleaned, l)
		}
		data = []byte(strings.Join(cleaned, "\n"))
		data = []byte(strings.TrimSpace(string(data)))
		var raw map[string]any
		if err := json.Unmarshal(data, &raw); err == nil {
			if cid, ok := raw["credential_id"].(string); ok {
				c.CredentialID = cid
				if pk, ok := raw["public_key"].(string); ok {
					c.PublicKey = pk
				}
			} else {
				// Look for passkeys array in role wrapper (full roles.json)
				for _, v := range raw {
					if m, ok := v.(map[string]any); ok {
						if pks, ok := m["passkeys"].([]any); ok && len(pks) > 0 {
							if pk0, ok := pks[0].(map[string]any); ok {
								c.CredentialID, _ = pk0["credential_id"].(string)
								c.PublicKey, _ = pk0["public_key"].(string)
								break
							}
						}
					}
				}
			}
		} else {
			// Try array form: RolesSnippet returns []map
			var arr []map[string]any
			if err2 := json.Unmarshal(data, &arr); err2 == nil && len(arr) > 0 {
				c.CredentialID, _ = arr[0]["credential_id"].(string)
				c.PublicKey, _ = arr[0]["public_key"].(string)
			} else {
				return fmt.Errorf("JSON không hợp lệ: %w", err)
			}
		}
	}
	if tui.HasControllingTTY() && (c.CredentialID == "" || c.PublicKey == "") {
		form := huh.NewForm(huh.NewGroup(
			huh.NewInput().Title("Credential ID base64url").Value(&c.CredentialID).Validate(nonEmpty),
			huh.NewInput().Title("Public key COSE base64url").Value(&c.PublicKey).Validate(nonEmpty),
		))
		if err := form.Run(); err != nil {
			return err
		}
	}
	if c.CredentialID == "" || c.PublicKey == "" {
		return errors.New("credential_id và public_key là bắt buộc (hoặc dùng --paste)")
	}
	root, _ := loadRolesMap()
	if _, exists := root[c.Role]; !exists {
		fmt.Printf("⚠️  Role \"%s\" chưa tồn tại — sẽ tạo mới\n", c.Role)
	}
	return identity.MergeRolesFile(rolesPath(), c.Role, func(e map[string]any) {
		list, _ := e["passkeys"].([]any)
		newEntry := map[string]any{
			"credential_id": c.CredentialID,
			"public_key":    c.PublicKey,
			"added_at":      time.Now().Format(time.RFC3339),
		}
		for i, raw := range list {
			if m, ok := raw.(map[string]any); ok && m["credential_id"] == c.CredentialID {
				list[i] = newEntry
				e["passkeys"] = list
				short := c.CredentialID
				if len(short) > 8 {
					short = short[:8]
				}
				fmt.Printf("⚠️  Đã ghi đè passkey %s trong role \"%s\"\n", short, c.Role)
				return
			}
		}
		e["passkeys"] = append(list, newEntry)
		if _, ok := e["identities"]; !ok {
			e["identities"] = []any{}
		}
		if _, ok := e["can_message_unlimited"]; !ok {
			e["can_message_unlimited"] = false
		}
		if _, ok := e["custom_prefix"]; !ok {
			e["custom_prefix"] = ""
		}
		fmt.Printf("✅ Đã thêm passkey vào role \"%s\"\n", c.Role)
	})
}

func (c *RoleImportCmd) Run() error {
	var data []byte
	var err error
	if c.File != "" {
		data, err = os.ReadFile(c.File)
		if err != nil {
			return err
		}
	} else if c.Paste {
		data, err = readPasteJSON()
		if err != nil {
			return err
		}
	} else {
		// Try stdin
		if tui.HasControllingTTY() {
			return errors.New("dùng --file <path> hoặc --paste để import")
		}
		data, err = os.ReadFile("/dev/stdin")
		if err != nil {
			return err
		}
	}
	var incoming map[string]any
	if err := json.Unmarshal(data, &incoming); err != nil {
		return fmt.Errorf("JSON không hợp lệ: %w", err)
	}
	root, err := loadRolesMap()
	if err != nil {
		return err
	}
	for role, v := range incoming {
		if _, exists := root[role]; exists && !c.Force {
			fmt.Printf("⚠️  Role \"%s\" đã tồn tại — bỏ qua (dùng --force để ghi đè)\n", role)
			continue
		}
		if _, exists := root[role]; exists {
			fmt.Printf("⚠️  Đã ghi đè role \"%s\"\n", role)
		} else {
			fmt.Printf("✅ Đã import role \"%s\"\n", role)
		}
		// Use MergeRolesFile to ensure atomic write per role, or write whole file at end
		// For efficiency, just set in root and write once at end
		root[role] = v
	}
	out, _ := json.MarshalIndent(root, "", "  ")
	return identity.AtomicWriteFile(rolesPath(), out, 0o600)
}


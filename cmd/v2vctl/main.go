package main

// v2vctl is the identity & server management tool split out of the chat client
// and the server per BLUEPRINTS.md.
//
// Layout mirrors real CLIs: `keygen` is a parent command with one leaf per
// identity flavor, so each flavor owns exactly the flags it needs.
//
// Interactive TTY sessions drive huh forms; scripts stay strictly
// flag-driven (defaults apply, hard errors on missing required values).
// The tool only touches local files: key.json, roles.json (opt-in merge)
// and the WEBAUTHN_STORE. No network, no daemon.

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/alecthomas/kong"
	"github.com/charmbracelet/huh"

	"github.com/CleveTok3125/V2V/identity"
)

var Version = "dev"

type CLI struct {
	Keygen  KeygenCmd  `cmd:"" help:"Tạo danh tính cá nhân vào key.json"`
	Role    RoleCmd    `cmd:"" help:"Quản lý role trong roles.json"`
	Enroll  EnrollCmd  `cmd:"" help:"Phát ticket enroll passkey thật (chạy trên host server)"`
	List    ListCmd    `cmd:"" help:"Xem tickets và credentials trong store"`
	Migrate MigrateCmd `cmd:"" help:"Đổi preset mã hóa cho key.json hiện có"`
}

type KeygenCmd struct {
	Ed25519 Ed25519Keygen `cmd:"" name:"ed25519" help:"Danh tính ed25519 key-file"`
	Passkey PasskeyKeygen `cmd:"" help:"Danh tính passkey mềm (WebAuthn wire format)"`
}

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
	if isInteractive() {
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
	if isInteractive() && c.Role == "" {
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
		if isInteractive() {
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
	if isInteractive() {
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
	if !c.Force && isInteractive() {
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
	if err := atomicWriteFileAdmin(rolesPath(), data, 0o600); err != nil {
		return err
	}
	fmt.Printf("✅ Đã xóa role \"%s\"\n", c.Role)
	return nil
}

func (c *RoleAddIdentityCmd) Run() error {
	if isInteractive() && c.Role == "" {
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
	if isInteractive() {
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
	if isInteractive() && c.Role == "" {
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
	if isInteractive() && (c.CredentialID == "" || c.PublicKey == "") {
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
		if isInteractive() {
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
	return atomicWriteFileAdmin(rolesPath(), out, 0o600)
}

var cli CLI

func main() {
	ctx := kong.Parse(&cli)
	ctx.FatalIfErrorf(ctx.Run())
}

// isInteractive reports whether a real controlling terminal exists. huh
// opens /dev/tty directly (it ignores piped stdin), so this must probe the
// tty device rather than os.Stdin.
func isInteractive() bool {
	tty, err := os.Open("/dev/tty")
	if err != nil {
		return false
	}
	_ = tty.Close()
	return true
}

func nonEmpty(s string) error {
	if strings.TrimSpace(s) == "" {
		return errors.New("bắt buộc")
	}
	return nil
}

func promptPassphrase() (string, error) {
	var pass string
	form := huh.NewForm(huh.NewGroup(
		huh.NewInput().Title("Passphrase (Enter = không mã hóa)").EchoMode(huh.EchoModePassword).Value(&pass),
	))
	if err := form.Run(); err != nil {
		return "", err
	}
	if strings.TrimSpace(pass) == "" {
		return "", nil
	}
	var confirm string
	form2 := huh.NewForm(huh.NewGroup(
		huh.NewInput().Title("Nhập lại passphrase").EchoMode(huh.EchoModePassword).Value(&confirm).Validate(func(s string) error {
			if s != pass {
				return errors.New("không khớp")
			}
			return nil
		}),
	))
	if err := form2.Run(); err != nil {
		return "", err
	}
	return pass, nil
}

func loadContainer(path string) (*identity.IdentityFile, error) {
	// Check if file is encrypted and need passphrase
	if enc, _ := identity.IsEncrypted(path); enc {
		// Try env first
		if pass := os.Getenv("V2V_PASSPHRASE"); pass != "" {
			return identity.LoadEncrypted(path, pass)
		}
		if isInteractive() {
			fmt.Println("🔒 File đã mã hóa, nhập passphrase để mở...")
			pass, err := promptPassphraseForLoad()
			if err != nil {
				return nil, err
			}
			return identity.LoadEncrypted(path, pass)
		}
		return nil, errors.New("key file is encrypted — set V2V_PASSPHRASE or run in TTY to unlock")
	}
	idf, err := identity.Load(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &identity.IdentityFile{}, nil
		}
		return nil, err
	}
	return idf, nil
}

func promptPassphraseForLoad() (string, error) {
	var pass string
	form := huh.NewForm(huh.NewGroup(
		huh.NewInput().Title("Nhập passphrase").EchoMode(huh.EchoModePassword).Value(&pass).Validate(nonEmpty),
	))
	if err := form.Run(); err != nil {
		return "", err
	}
	return pass, nil
}

func saveContainer(idf *identity.IdentityFile, path string) error {
	if isInteractive() {
		pass, err := promptPassphrase()
		if err != nil {
			return err
		}
		if pass != "" {
			return idf.SaveEncrypted(path, pass, nil)
		}
	}
	// Check env for non-interactive
	if pass := os.Getenv("V2V_PASSPHRASE"); pass != "" {
		return idf.SaveEncrypted(path, pass, nil)
	}
	return idf.Save(path)
}

func rolesPath() string { return "roles.json" }

// --- keygen ed25519 -----------------------------------------------------

func (c *Ed25519Keygen) Run() error {
	if isInteractive() {
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
	if isInteractive() {
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

type EnrollCmd struct {
	Role  string        `help:"Role gắn với passkey" default:"member"`
	Label string        `help:"Nhãn thiết bị/người"`
	Store string        `help:"Đường dẫn store" default:"data/webauthn.json" env:"WEBAUTHN_STORE"`
	TTL   time.Duration `help:"Thời gian hiệu lực ticket" default:"10m"`
}

func (e *EnrollCmd) Run() error {
	if isInteractive() {
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
	origin := os.Getenv("WEBAUTHN_ORIGIN")
	fmt.Println("✅ Ticket đã tạo (single-use, TTL " + e.TTL.String() + ").")
	fmt.Printf("Gửi link sau cho người được cấp:\n\n  %s/web/#enroll=%s\n\n", origin, code)
	return nil
}

// --- migrate ----------------------------------------------------------------

type MigrateCmd struct {
	In     string `help:"File nguồn" default:"key.json"`
	Out    string `help:"File đích (mặc định ghi đè In, backup .old)"`
	Preset string `help:"Preset đích: native, wasm, custom" default:"native"`
	Force  bool   `help:"Ghi đè không hỏi khi .old đã tồn tại"`
}

func (m *MigrateCmd) Run() error {
	inPath := m.In
	outPath := m.Out
	if outPath == "" {
		outPath = inPath
	}
	// Detect current file status
	enc, _ := identity.IsEncrypted(inPath)
	var curPreset string
	if enc {
		data, _ := os.ReadFile(inPath)
		var env struct {
			Encrypted struct {
				Memory uint32 `json:"m"`
				Time   uint32 `json:"t"`
			} `json:"encrypted"`
		}
		_ = json.Unmarshal(data, &env)
		if env.Encrypted.Memory == 32768 {
			curPreset = "wasm (t=1,m=32MB)"
		} else {
			curPreset = fmt.Sprintf("native (t=%d,m=%d)", env.Encrypted.Time, env.Encrypted.Memory)
		}
	} else {
		curPreset = "plaintext (không mã hóa)"
	}

	if isInteractive() {
		// Distinct TUI: show current status, then choose preset, then passphrases
		var presetChoice string
		form := huh.NewForm(huh.NewGroup(
			huh.NewSelect[string]().Title("Preset hiện tại: "+curPreset+" → Chọn preset đích").Options(
				huh.NewOption("Native — t=3,m=64MB, XChaCha (mạnh)", "native"),
				huh.NewOption("WASM — t=1,m=32MB, XChaCha (nhanh, cho browser)", "wasm"),
				huh.NewOption("Custom", "custom"),
			).Value(&presetChoice),
		))
		if err := form.Run(); err != nil {
			return err
		}
		if presetChoice != "" {
			m.Preset = presetChoice
		}
	}

	// Handle In -> .old backup
	oldPath := inPath + ".old"
	if _, err := os.Stat(oldPath); err == nil {
		if !m.Force {
			if isInteractive() {
				var overwrite bool
				confirm := huh.NewForm(huh.NewGroup(
					huh.NewConfirm().Title(fmt.Sprintf("%s đã tồn tại — ghi đè?", oldPath)).Affirmative("Ghi đè").Negative("Hủy").Value(&overwrite),
				))
				if err := confirm.Run(); err != nil {
					return err
				}
				if !overwrite {
					return errors.New("hủy migrate")
				}
			} else {
				return fmt.Errorf("%s đã tồn tại, dùng --force để ghi đè", oldPath)
			}
		}
		fmt.Printf("⚠️  Ghi đè %s\n", oldPath)
	}

	// Load with old passphrase if needed
	var idf *identity.IdentityFile
	var oldPass string
	if enc {
		if p := os.Getenv("V2V_PASSPHRASE"); p != "" {
			oldPass = p
		} else if isInteractive() {
			fmt.Println("🔒 File đã mã hóa, nhập passphrase hiện tại để mở...")
			var err error
			oldPass, err = promptPassphraseForLoad()
			if err != nil {
				return err
			}
		} else {
			return errors.New("key file is encrypted — set V2V_PASSPHRASE")
		}
		var err error
		idf, err = identity.LoadEncrypted(inPath, oldPass)
		if err != nil {
			return fmt.Errorf("sai passphrase hoặc file hỏng: %w", err)
		}
	} else {
		var err error
		idf, err = identity.Load(inPath)
		if err != nil {
			return err
		}
	}

	// Determine new preset
	var p *identity.Params
	switch m.Preset {
	case "wasm":
		pp := identity.PresetWASM
		p = &pp
	case "native":
		pp := identity.PresetNative
		p = &pp
	case "custom":
		// For custom, prompt for t/m/p if interactive
		if isInteractive() {
			var tStr, mStr string
			form := huh.NewForm(huh.NewGroup(
				huh.NewInput().Title("Time (t)").Value(&tStr),
				huh.NewInput().Title("Memory KiB (m)").Value(&mStr),
			))
			if err := form.Run(); err != nil {
				return err
			}
			// Parse and set custom, fallback to native if invalid
			pp := identity.PresetNative
			if tStr != "" {
				fmt.Sscanf(tStr, "%d", &pp.Time)
			}
			if mStr != "" {
				fmt.Sscanf(mStr, "%d", &pp.Memory)
			}
			p = &pp
		}
	default:
		pp := identity.PresetNative
		p = &pp
	}

	// Prompt for new passphrase if target is encrypted (preset != plaintext)
	// For migrate, we always re-encrypt (unless user wants plaintext)
	var newPass string
	if isInteractive() {
		fmt.Println("🔒 Nhập passphrase mới cho file đích (Enter = không mã hóa):")
		var err error
		newPass, err = promptPassphrase()
		if err != nil {
			return err
		}
	} else if pass := os.Getenv("V2V_PASSPHRASE"); pass != "" {
		newPass = pass
	}

	// Write to temp out path first
	tmpOut := outPath
	if outPath == inPath {
		tmpOut = inPath + ".tmp-migrate"
	}
	var err error
	if newPass != "" {
		err = idf.SaveEncrypted(tmpOut, newPass, p)
	} else {
		err = idf.Save(tmpOut)
	}
	if err != nil {
		return err
	}

	// Atomic In -> .old and Out -> In
	if err := os.Rename(inPath, oldPath); err != nil {
		os.Remove(tmpOut)
		return fmt.Errorf("không thể backup %s -> %s: %w", inPath, oldPath, err)
	}
	if err := os.Rename(tmpOut, inPath); err != nil {
		// Try to restore backup on failure
		_ = os.Rename(oldPath, inPath)
		os.Remove(tmpOut)
		return err
	}
	// Fsync dir
	if dirFile, err := os.Open(filepath.Dir(inPath)); err == nil {
		_ = dirFile.Sync()
		dirFile.Close()
	}
	fmt.Printf("✅ Đã migrate %s -> %s (backup %s), preset %s\n", inPath, inPath, oldPath, m.Preset)
	return nil
}

// --- list -----------------------------------------------------------------

 // waPending mirrors the server's pending-ticket schema.
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

func short(code string) string {
	if len(code) > 12 {
		return code[:12] + "…"
	}
	return code
}

type ListCmd struct {
	Store string `help:"Đường dẫn store" default:"data/webauthn.json" env:"WEBAUTHN_STORE"`
}

func atomicWriteFileAdmin(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
	}
	tmpFile, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmpFile.Name()
	if err := tmpFile.Chmod(perm); err != nil {
		tmpFile.Close()
		os.Remove(tmpName)
		return err
	}
	if _, err := tmpFile.Write(data); err != nil {
		tmpFile.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmpFile.Sync(); err != nil {
		tmpFile.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmpFile.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		os.Remove(tmpName)
		return err
	}
	if dirFile, err := os.Open(dir); err == nil {
		_ = dirFile.Sync()
		dirFile.Close()
	}
	return nil
}

func saveStore(path string, f *waStoreFile) error {
	out, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	return atomicWriteFileAdmin(path, out, 0o600)
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
			state, p.Role, p.Label, p.ExpiresAt.Format(time.RFC3339), short(p.Code))
	}
	fmt.Println("== Credentials ==")
	for role, creds := range f.Credentials {
		for _, c := range creds {
			fmt.Printf("  role=%s cred=%s… added=%v\n", role, short(str(c["credential_id"])), str(c["added_at"]))
		}
	}
	return nil
}

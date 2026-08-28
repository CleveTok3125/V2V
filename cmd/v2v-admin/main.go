package main

// v2v-admin is the identity management tool split out of the chat client
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

	"localchat/identity"
)

type CLI struct {
	Keygen KeygenCmd `cmd:"" help:"Tạo danh tính cá nhân vào key.json"`
	Enroll EnrollCmd `cmd:"" help:"Phát ticket enroll passkey thật (chạy trên host server)"`
	List   ListCmd   `cmd:"" help:"Xem tickets và credentials trong store"`
}

type KeygenCmd struct {
	Ed25519 Ed25519Keygen `cmd:"" name:"ed25519" help:"Danh tính ed25519 key-file"`
	Passkey PasskeyKeygen `cmd:"" help:"Danh tính passkey mềm (WebAuthn wire format)"`
}

type Ed25519Keygen struct {
	Role       string `help:"Role gắn với danh tính" default:"admin"`
	Out        string `help:"Nơi ghi container" default:"key.json"`
	Unlimited  bool   `help:"Quyền chat không giới hạn"`
	Prefix     string `help:"Prefix hiển thị" default:"[Admin] "`
	Host       string `help:"Hostname gắn với danh tính (chống dùng chéo deployment)"`
	MergeRoles bool   `help:"Ghép entry vào ./roles.json (mặc định chỉ in snippet)"`
}

type PasskeyKeygen struct {
	Role       string `help:"Role gắn với passkey" default:"member"`
	Out        string `help:"Nơi ghi container" default:"key.json"`
	RPID       string `help:"RP ID; fallback env WEBAUTHN_RPID"`
	Origin     string `help:"Origin; fallback env WEBAUTHN_ORIGIN"`
	Label      string `help:"Nhãn thiết bị/người"`
	MergeRoles bool   `help:"Ghép credential vào ./roles.json (mặc định chỉ in snippet)"`
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
		form := huh.NewForm(huh.NewGroup(
			huh.NewInput().Title("Role").Value(&c.Role).Validate(nonEmpty),
			huh.NewInput().Title("Nơi lưu key.json").Value(&c.Out),
			huh.NewConfirm().Title("Quyền chat không giới hạn?").Value(&c.Unlimited),
			huh.NewInput().Title("Prefix hiển thị").Value(&c.Prefix),
			huh.NewInput().Title("Hostname gắn với danh tính (Enter = không gắn)").Value(&c.Host),
			huh.NewConfirm().Title("Ghép entry vào roles.json ngay bây giờ?").
				Affirmative("Có").Negative("Không").Value(&c.MergeRoles),
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
		Role:       c.Role,
		PrivateKey: hex.EncodeToString(priv),
		HmacShield: hex.EncodeToString(shield),
		Host:       c.Host,
	}
	if err := saveContainer(idf, c.Out); err != nil {
		return err
	}
	fmt.Printf("\n💾 Đã lưu khóa bí mật tại %s (chmod 600)\n", c.Out)

	if !c.MergeRoles {
		serverCfg := map[string]any{
			c.Role: map[string]any{
				"identities": []map[string]string{{
					"public_key":  hex.EncodeToString(pub),
					"hmac_shield": hex.EncodeToString(shield),
					"host":        c.Host,
				}},
				"can_message_unlimited": c.Unlimited,
				"custom_prefix":         c.Prefix,
			},
		}
		out, _ := json.MarshalIndent(serverCfg, "", "  ")
		fmt.Print("📤 Gửi đoạn sau cho admin thêm vào roles.json:\n\n")
		fmt.Println(string(out))
		return nil
	}
	return identity.MergeRolesFile(rolesPath(), c.Role, func(e map[string]any) {
		e["identities"] = []map[string]string{{
			"public_key":  hex.EncodeToString(pub),
			"hmac_shield": hex.EncodeToString(shield),
			"host":        c.Host,
		}}
		e["can_message_unlimited"] = c.Unlimited
		e["custom_prefix"] = c.Prefix
	})
}

// --- keygen passkey (mềm) -----------------------------------------------

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
			huh.NewConfirm().Title("Ghép credential vào roles.json ngay bây giờ?").
				Affirmative("Có").Negative("Không").Value(&c.MergeRoles),
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
	fmt.Print("📤 Gửi đoạn sau cho admin thêm vào roles.json:\n\n")
	fmt.Println(snippet)

	if !c.MergeRoles {
		return nil
	}
	return pk.MergePasskeyCredential(rolesPath(), c.Role)
}

// --- enroll ---------------------------------------------------------------

type EnrollCmd struct {
	Role      string        `help:"Role gắn với passkey" default:"member"`
	Label     string        `help:"Nhãn thiết bị/người"`
	Unlimited bool          `help:"Quyền chat không giới hạn"`
	Prefix    string        `help:"Prefix hiển thị" default:"[Member] "`
	Store     string        `help:"Đường dẫn store" default:"data/webauthn.json" env:"WEBAUTHN_STORE"`
	TTL       time.Duration `help:"Thời gian hiệu lực ticket" default:"10m"`
}

func (e *EnrollCmd) Run() error {
	if isInteractive() {
		form := huh.NewForm(huh.NewGroup(
			huh.NewInput().Title("Role").Value(&e.Role).Validate(nonEmpty),
			huh.NewConfirm().Title("Quyền chat không giới hạn?").Value(&e.Unlimited),
			huh.NewInput().Title("Prefix hiển thị").Value(&e.Prefix),
			huh.NewInput().Title("Nhãn thiết bị/người (tùy chọn)").Value(&e.Label),
		))
		if err := form.Run(); err != nil {
			return err
		}
	}
	if e.Prefix == "" {
		e.Prefix = "[Member] "
	}
	// Merge role permissions into roles.json (report duplicate, overwrite optionally)
	rolesData, _ := os.ReadFile(rolesPath())
	var rolesRoot map[string]any
	if len(rolesData) > 0 {
		_ = json.Unmarshal(rolesData, &rolesRoot)
	}
	if rolesRoot == nil {
		rolesRoot = map[string]any{}
	}
	if _, exists := rolesRoot[e.Role]; exists {
		fmt.Printf("⚠️  Role \"%s\" đã tồn tại trong roles.json — sẽ ghi đè quyền hạn.\n", e.Role)
	}
	if err := identity.MergeRolesFile(rolesPath(), e.Role, func(entry map[string]any) {
		entry["can_message_unlimited"] = e.Unlimited
		entry["custom_prefix"] = e.Prefix
		if _, ok := entry["identities"]; !ok {
			entry["identities"] = []map[string]string{}
		}
		if _, ok := entry["passkeys"]; !ok {
			entry["passkeys"] = []any{}
		}
	}); err != nil {
		return err
	}
	if _, exists := rolesRoot[e.Role]; !exists {
		fmt.Printf("✅ Đã thêm role \"%s\" vào roles.json\n", e.Role)
	} else {
		fmt.Printf("✅ Đã cập nhật quyền hạn cho role \"%s\" trong roles.json\n", e.Role)
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

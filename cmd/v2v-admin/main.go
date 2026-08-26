package main

// v2v-admin is the identity management tool split out of the chat client
// and the server per BLUEPRINTS.md:
//
//	v2v-admin keygen  — create a personal identity container (client machine)
//	v2v-admin enroll  — issue a one-time passkey enrollment ticket (server host)
//	v2v-admin list    — inspect pending tickets and stored credentials
//
// It only touches local files: key.json, roles.json (with --merge-roles) and
// the WEBAUTHN_STORE. No network, no daemon.

import (
	"bufio"
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

	"localchat/identity"
)

type CLI struct {
	Keygen KeygenCmd `cmd:"" help:"Tạo danh tính cá nhân vào key.json"`
	Enroll EnrollCmd `cmd:"" help:"Phát ticket enroll passkey thật (chạy trên host server)"`
	List   ListCmd   `cmd:"" help:"Xem tickets và credentials trong store"`
}

type KeygenCmd struct {
	Role       string `help:"Role gắn với danh tính (hỏi nếu bỏ trống)"`
	Type       string `help:"Loại danh tính: ed25519 | passkey (hỏi nếu bỏ trống)"`
	Out        string `help:"Nơi ghi container" default:"key.json"`
	Unlimited  bool   `help:"(ed25519) quyền chat không giới hạn"`
	Prefix     string `help:"(ed25519) prefix hiển thị"`
	Host       string `help:"(ed25519) hostname gắn với danh tính (trống = không gắn)"`
	RPID       string `help:"(passkey) RP ID; fallback env WEBAUTHN_RPID"`
	Origin     string `help:"(passkey) Origin; fallback env WEBAUTHN_ORIGIN"`
	MergeRoles bool   `help:"Ghép entry vào ./roles.json (mặc định chỉ in snippet)"`
	Label      string `help:"(passkey) nhãn thiết bị/người"`
}

type EnrollCmd struct {
	Role  string        `help:"Role gắn với passkey" required:""`
	Label string        `help:"Nhãn thiết bị/người"`
	Store string        `help:"Đường dẫn store" default:"data/webauthn.json" env:"WEBAUTHN_STORE"`
	TTL   time.Duration `help:"Thời gian hiệu lực ticket" default:"10m"`
}

type ListCmd struct {
	Store string `help:"Đường dẫn store" default:"data/webauthn.json" env:"WEBAUTHN_STORE"`
}

var cli CLI

func main() {
	ctx := kong.Parse(&cli)
	ctx.FatalIfErrorf(ctx.Run())
}

// isInteractive reports whether stdin is a terminal worth prompting on.
func isInteractive() bool {
	st, err := os.Stdin.Stat()
	return err == nil && st.Mode()&os.ModeCharDevice != 0
}

func prompt(reader *bufio.Reader, msg string) string {
	fmt.Print(msg + ": ")
	line, _ := reader.ReadString('\n')
	return strings.TrimSpace(line)
}

func ask(reader *bufio.Reader, msg, def string) string {
	line := prompt(reader, msg+" ["+def+"]: ")
	if line == "" {
		return def
	}
	return line
}

func loadIdentityContainer(path string) (*identity.IdentityFile, error) {
	idf, err := identity.Load(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &identity.IdentityFile{}, nil
		}
		return nil, err
	}
	return idf, nil
}

// mergePasskeyCredential appends (or dedupe-updates) a credential entry in
// the role's passkeys[] while preserving everything else in roles.json.
func mergePasskeyCredential(path, role, credID, coseB64 string) error {
	return identity.MergeRolesFile(path, role, func(entry map[string]any) {
		newEntry := map[string]any{
			"credential_id": credID,
			"public_key":    coseB64,
			"added_at":      time.Now().Format(time.RFC3339),
		}
		list, _ := entry["passkeys"].([]any)
		for i, raw := range list {
			if ex, _ := raw.(map[string]any); ex != nil && ex["credential_id"] == credID {
				list[i] = newEntry
				return
			}
		}
		entry["passkeys"] = append(list, newEntry)
	})
}

func (k *KeygenCmd) Run() error {
	interactive := isInteractive()
	reader := bufio.NewReader(os.Stdin)

	if k.Role == "" {
		if !interactive {
			k.Role = "admin"
		} else {
			k.Role = ask(reader, "Role", "admin")
		}
	}
	if k.Type == "" {
		if !interactive {
			k.Type = "ed25519"
		} else {
			for k.Type != "ed25519" && k.Type != "passkey" {
				k.Type = ask(reader, "Loại danh tính — ed25519 | passkey", "ed25519")
			}
		}
	}
	switch k.Type {
	case "ed25519":
	default:
		k.Type = "ed25519"
	}

	rpid, origin := k.RPID, k.Origin
	if k.Type == "passkey" && (rpid == "" || origin == "") {
		envR, envO := os.Getenv("WEBAUTHN_RPID"), os.Getenv("WEBAUTHN_ORIGIN")
		if rpid == "" {
			rpid = envR
		}
		if origin == "" {
			origin = envO
		}
		if (rpid == "" || origin == "") && interactive {
			if rpid == "" {
				rpid = prompt(reader, "RP ID (vd: chat.example.com)")
			}
			if origin == "" {
				origin = prompt(reader, "Origin (vd: https://chat.example.com)")
			}
		}
		if rpid == "" || origin == "" {
			return errors.New("--type passkey cần --rpid và --origin (hoặc env WEBAUTHN_RPID/WEBAUTHN_ORIGIN)")
		}
	}

	idf := &identity.IdentityFile{}
	var edPubHex string
	switch k.Type {
	case "ed25519":
		if k.Prefix == "" {
			k.Prefix = "[Member] "
		}
		if k.Host == "" && interactive {
			k.Host = ask(reader, "Hostname gắn với danh tính (Enter = không gắn)", "")
		}
		pub, priv, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			return err
		}
		shieldBytes := make([]byte, 16)
		if _, err := rand.Read(shieldBytes); err != nil {
			return err
		}
		edPubHex = hex.EncodeToString(pub)
		idf.Ed25519 = &identity.Ed25519Identity{
			Role:       k.Role,
			PrivateKey: hex.EncodeToString(priv),
			HmacShield: hex.EncodeToString(shieldBytes),
			Host:       k.Host,
		}
	case "passkey":
		pk, err := identity.GeneratePasskey(k.Role, rpid, origin)
		if err != nil {
			return err
		}
		idf.Passkey = pk
	}

	if err := idf.Save(k.Out); err != nil {
		return err
	}
	fmt.Printf("\n💾 Đã lưu khóa bí mật tại %s (chmod 600)\n", k.Out)

	switch k.Type {
	case "passkey":
		snippet, serr := idf.Passkey.RolesSnippet()
		if serr != nil {
			return serr
		}
		fmt.Print("📤 Gửi đoạn sau cho admin thêm vào roles.json:\n\n")
		fmt.Println(snippet)
	default:
		identityEntry := map[string]string{
			"public_key":  edPubHex,
			"hmac_shield": idf.Ed25519.HmacShield,
		}
		if idf.Ed25519.Host != "" {
			identityEntry["host"] = idf.Ed25519.Host
		}
		serverCfg := map[string]any{
			k.Role: map[string]any{
				"identities":            []map[string]string{identityEntry},
				"can_message_unlimited": k.Unlimited,
				"custom_prefix":         k.Prefix,
			},
		}
		out, _ := json.MarshalIndent(serverCfg, "", "  ")
		fmt.Print("📤 Gửi đoạn sau cho admin thêm vào roles.json:\n\n")
		fmt.Println(string(out))
	}

	if k.MergeRoles && k.Type == "passkey" {
		if err := mergePasskeyCredential(rolesPath(), k.Role, idf.Passkey.CredentialID, idf.Passkey.PublicKey); err != nil {
			return err
		}
		fmt.Println("Đã cập nhật ./roles.json")
	} else if k.MergeRoles && k.Type == "ed25519" {
		if err := identity.MergeRolesFile(rolesPath(), k.Role, func(e map[string]any) {
			identityEntry := map[string]string{
				"public_key":  edPubHex,
				"hmac_shield": idf.Ed25519.HmacShield,
			}
			if idf.Ed25519.Host != "" {
				identityEntry["host"] = idf.Ed25519.Host
			}
			e["identities"] = []map[string]string{identityEntry}
			e["can_message_unlimited"] = k.Unlimited
			e["custom_prefix"] = k.Prefix
		}); err != nil {
			return err
		}
		fmt.Println("Đã cập nhật ./roles.json")
	}
	return nil
}

func rolesPath() string { return "roles.json" }

// --- enrollment tickets -------------------------------------------------

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

func saveStore(path string, f *waStoreFile) error {
	out, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, out, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func (e *EnrollCmd) Run() error {
	origin := os.Getenv("WEBAUTHN_ORIGIN")
	if origin == "" {
		return errors.New("WEBAUTHN_ORIGIN chưa đặt trong .env — không tạo được URL enroll")
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
	fmt.Println("✅ Ticket đã tạo (single-use, TTL " + e.TTL.String() + ").")
	fmt.Printf("Gửi link sau cho người được cấp:\n\n  %s/web/#enroll=%s\n\n", origin, code)
	return nil
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

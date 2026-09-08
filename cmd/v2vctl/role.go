package main

import (
	"errors"
	"fmt"
	"os"
	"strings"
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

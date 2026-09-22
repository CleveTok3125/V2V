package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/CleveTok3125/V2V/internal/identity"
)

// InstanceCmd manages per-environment instances: each is a directory under
// instances/ holding .env, config/ and data/, run from the same image with
// a distinct compose project name. No preset, no override file.
type InstanceCmd struct {
	Init    InstanceInitCmd    `cmd:"" help:"Tạo instance mới từ template (roles để trống, thêm bằng v2vctl role)"`
	List    InstanceListCmd    `cmd:"" help:"Liệt kê các instance"`
	Status  InstanceStatusCmd  `cmd:"" help:"Trạng thái compose của instance"`
	Up      InstanceUpCmd      `cmd:"" help:"Khởi động instance (docker compose up)"`
	Down    InstanceDownCmd    `cmd:"" help:"Dừng instance"`
	Restart InstanceRestartCmd `cmd:"" help:"Khởi động lại instance"`
	Logs    InstanceLogsCmd    `cmd:"" help:"Xem log instance"`
}

var instanceNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)

func validateInstanceName(name string) error {
	if !instanceNamePattern.MatchString(name) {
		return fmt.Errorf("tên instance %q không hợp lệ (chữ thường, số, gạch ngang; 1-32 ký tự)", name)
	}
	return nil
}

// projectRoot walks up from dir until it finds the manifest.
func projectRoot(dir string) (string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	for d := abs; ; {
		if _, err := os.Stat(filepath.Join(d, manifestName)); err == nil {
			return d, nil
		}
		parent := filepath.Dir(d)
		if parent == d {
			return "", fmt.Errorf("không tìm thấy %s từ %s", manifestName, abs)
		}
		d = parent
	}
}

// instanceDir resolves the instance directory under the project root.
func instanceDir(root, name string) string {
	return filepath.Join(root, "instances", name)
}

// setEnvValue sets one active KEY=value in a .env file, replacing or
// uncommenting an existing line and appending when absent.
func setEnvValue(path, key, value string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	found := false
	for i, line := range lines {
		body := strings.TrimSpace(line)
		if strings.HasPrefix(body, "#") {
			body = strings.TrimSpace(strings.TrimLeft(body, "#"))
		}
		eq := strings.Index(body, "=")
		if eq < 0 || strings.TrimSpace(body[:eq]) != key {
			continue
		}
		lines[i] = key + "=" + value
		found = true
		break
	}
	if !found {
		lines = append(lines, key+"="+value)
	}
	return identity.WriteConfigFile(path, []byte(strings.Join(lines, "\n")+"\n"))
}

type InstanceInitCmd struct {
	Name string `arg:"" help:"Tên instance"`
	Dir  string `help:"Thư mục dự án (chứa v2v-template.json)" default:"."`
	Port int    `help:"Giá trị PORT ghi vào .env (1-65535; 0 = giữ template)"`
	Bind string `help:"Giá trị BIND_ADDR ghi vào .env"`
}

func (c *InstanceInitCmd) Run() error {
	if err := validateInstanceName(c.Name); err != nil {
		return err
	}
	if c.Port != 0 && (c.Port < 1 || c.Port > 65535) {
		return fmt.Errorf("PORT %d không hợp lệ (1-65535)", c.Port)
	}
	root, err := projectRoot(c.Dir)
	if err != nil {
		return err
	}
	inst := instanceDir(root, c.Name)
	if _, err := os.Stat(inst); err == nil {
		return fmt.Errorf("instance %q đã tồn tại tại %s", c.Name, inst)
	}
	// Remove a partial instance if anything below fails, so a retry is
	// not blocked by a half-written directory.
	complete := false
	defer func() {
		if !complete {
			_ = os.RemoveAll(inst)
		}
	}()
	sync := &ConfigSyncCmd{
		ConfigCommon: ConfigCommon{Dir: root, To: inst, Only: "env,roles,trust", NoPager: true},
		Quiet:        true,
	}
	if err := sync.Run(); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(inst, "data"), 0o755); err != nil {
		return err
	}
	envPath := filepath.Join(inst, ".env")
	if c.Port > 0 {
		if err := setEnvValue(envPath, "PORT", fmt.Sprintf("%d", c.Port)); err != nil {
			return err
		}
	}
	if strings.TrimSpace(c.Bind) != "" {
		if err := setEnvValue(envPath, "BIND_ADDR", strings.TrimSpace(c.Bind)); err != nil {
			return err
		}
	}
	fmt.Println("instance ready: " + inst)
	complete = true
	return nil
}

type InstanceListCmd struct {
	Dir string `help:"Thư mục dự án" default:"."`
}

func (c *InstanceListCmd) Run() error {
	root, err := projectRoot(c.Dir)
	if err != nil {
		return err
	}
	entries, err := os.ReadDir(filepath.Join(root, "instances"))
	if err != nil {
		fmt.Println("chưa có instance nào")
		return nil
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	for _, n := range names {
		state := "no .env"
		if _, err := os.Stat(filepath.Join(instanceDir(root, n), ".env")); err == nil {
			state = "ready"
		}
		fmt.Printf("%s\t%s\n", n, state)
	}
	if len(names) == 0 {
		fmt.Println("chưa có instance nào")
	}
	return nil
}

// composeRunner runs one docker compose command. Swapped in tests.
var composeRunner = defaultComposeRunner

func defaultComposeRunner(args, env []string, dir string) error {
	path, err := exec.LookPath("docker")
	if err != nil {
		return fmt.Errorf("docker không có sẵn: %w", err)
	}
	cmd := exec.Command(path, args...)
	cmd.Env = env
	cmd.Dir = dir
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// runInstanceCompose builds and runs the compose command for one instance,
// exporting ENV_ROOT so the base compose mounts the right directory.
func runInstanceCompose(root, name string, sub ...string) error {
	if err := validateInstanceName(name); err != nil {
		return err
	}
	inst := instanceDir(root, name)
	if _, err := os.Stat(filepath.Join(inst, ".env")); err != nil {
		return fmt.Errorf("instance %q chưa có .env: %w", name, err)
	}
	args := []string{
		"compose",
		"--project-name", "v2v-" + name,
		"--env-file", filepath.Join(inst, ".env"),
		"-f", filepath.Join(root, "docker-compose.yml"),
	}
	args = append(args, sub...)
	env := append(os.Environ(), "ENV_ROOT="+inst)
	return composeRunner(args, env, root)
}

type InstanceStatusCmd struct {
	Name string `arg:"" optional:"" help:"Tên instance (bỏ trống = tất cả)"`
	Dir  string `help:"Thư mục dự án" default:"."`
}

func (c *InstanceStatusCmd) Run() error {
	root, err := projectRoot(c.Dir)
	if err != nil {
		return err
	}
	names, err := instanceNames(root, c.Name)
	if err != nil {
		return err
	}
	for _, n := range names {
		if err := runInstanceCompose(root, n, "ps"); err != nil {
			return err
		}
	}
	return nil
}

type InstanceUpCmd struct {
	Name  string `arg:"" help:"Tên instance"`
	Dir   string `help:"Thư mục dự án" default:"."`
	Build bool   `help:"Build image trước khi chạy" default:"true"`
}

func (c *InstanceUpCmd) Run() error {
	root, err := projectRoot(c.Dir)
	if err != nil {
		return err
	}
	sub := []string{"up", "-d"}
	if c.Build {
		sub = append(sub, "--build")
	}
	return runInstanceCompose(root, c.Name, sub...)
}

type InstanceDownCmd struct {
	Name string `arg:"" help:"Tên instance"`
	Dir  string `help:"Thư mục dự án" default:"."`
}

func (c *InstanceDownCmd) Run() error {
	root, err := projectRoot(c.Dir)
	if err != nil {
		return err
	}
	return runInstanceCompose(root, c.Name, "down")
}

type InstanceRestartCmd struct {
	Name string `arg:"" help:"Tên instance"`
	Dir  string `help:"Thư mục dự án" default:"."`
}

func (c *InstanceRestartCmd) Run() error {
	root, err := projectRoot(c.Dir)
	if err != nil {
		return err
	}
	return runInstanceCompose(root, c.Name, "restart")
}

type InstanceLogsCmd struct {
	Name   string `arg:"" help:"Tên instance"`
	Dir    string `help:"Thư mục dự án" default:"."`
	Follow bool   `help:"Theo dõi log" short:"f"`
}

func (c *InstanceLogsCmd) Run() error {
	root, err := projectRoot(c.Dir)
	if err != nil {
		return err
	}
	sub := []string{"logs"}
	if c.Follow {
		sub = append(sub, "-f")
	}
	return runInstanceCompose(root, c.Name, sub...)
}

// instanceNames returns the requested instance, or every instance when
// name is empty.
func instanceNames(root, name string) ([]string, error) {
	if strings.TrimSpace(name) != "" {
		if err := validateInstanceName(name); err != nil {
			return nil, err
		}
		return []string{name}, nil
	}
	entries, err := os.ReadDir(filepath.Join(root, "instances"))
	if err != nil {
		return nil, fmt.Errorf("chưa có instance nào: %w", err)
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	if len(names) == 0 {
		return nil, fmt.Errorf("chưa có instance nào")
	}
	return names, nil
}

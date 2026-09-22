package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSetEnvValue(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(path, []byte("#BIND_ADDR=127.0.0.1\nPORT=10000\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := setEnvValue(path, "BIND_ADDR", "0.0.0.0"); err != nil {
		t.Fatal(err)
	}
	if err := setEnvValue(path, "PORT", "10001"); err != nil {
		t.Fatal(err)
	}
	if err := setEnvValue(path, "NEW_KEY", "x"); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	out := string(data)
	for _, want := range []string{"BIND_ADDR=0.0.0.0\n", "PORT=10001\n", "NEW_KEY=x\n"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "#BIND_ADDR") {
		t.Fatalf("BIND_ADDR must be uncommented, got:\n%s", out)
	}
}

func TestInstanceInitRejectsBadPort(t *testing.T) {
	dir, _ := fixture(t)
	if err := (&InstanceInitCmd{Name: "prod", Dir: dir, Port: 99999}).Run(); err == nil {
		t.Fatal("out-of-range port must be rejected")
	}
	if err := (&InstanceInitCmd{Name: "Prod", Dir: dir}).Run(); err == nil {
		t.Fatal("invalid name must be rejected")
	}
}

func TestInstanceListAndNames(t *testing.T) {
	dir, _ := fixture(t)
	for _, n := range []string{"b", "a"} {
		if err := (&InstanceInitCmd{Name: n, Dir: dir}).Run(); err != nil {
			t.Fatal(err)
		}
	}
	names, err := instanceNames(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 2 || names[0] != "a" || names[1] != "b" {
		t.Fatalf("names = %v, want [a b]", names)
	}
	if got, err := instanceNames(dir, "a"); err != nil || len(got) != 1 {
		t.Fatalf("single name = %v %v", got, err)
	}
	if _, err := instanceNames(dir, "nope/../x"); err == nil {
		t.Fatal("traversal name must be rejected")
	}
}

func TestRunInstanceComposeGuards(t *testing.T) {
	dir, _ := fixture(t)
	if err := runInstanceCompose(dir, "missing"); err == nil {
		t.Fatal("instance without .env must fail")
	}
	if err := runInstanceCompose(dir, "bad/name"); err == nil {
		t.Fatal("invalid instance name must fail")
	}
}

func TestDefaultManifestPaths(t *testing.T) {
	m := defaultManifest()
	want := map[string]string{
		"env":    "server/instances/default/.env",
		"roles":  "server/instances/default/config/roles.json",
		"trust":  "server/instances/default/config/trustedproxy",
		"client": "client/config.jsonc",
	}
	for _, f := range m.Files {
		if want[f.ID] != f.Path {
			t.Fatalf("%s path = %q, want %q", f.ID, f.Path, want[f.ID])
		}
	}
}

func TestValidateInstanceName(t *testing.T) {
	for _, ok := range []string{"default", "prod", "a1", "v2-prod-2"} {
		if err := validateInstanceName(ok); err != nil {
			t.Fatalf("%q must be valid: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "Prod", "a_b", "../x", "a/b", strings.Repeat("a", 40)} {
		if err := validateInstanceName(bad); err == nil {
			t.Fatalf("%q must be rejected", bad)
		}
	}
}

func TestInstanceInitCreatesTree(t *testing.T) {
	dir, _ := fixture(t)
	c := &InstanceInitCmd{Name: "prod", Dir: dir, Port: 10001, Bind: "0.0.0.0"}
	if err := c.Run(); err != nil {
		t.Fatal(err)
	}
	inst := filepath.Join(dir, "instances", "prod")
	for _, rel := range []string{".env", filepath.Join("config", "roles.json"), "data"} {
		if _, err := os.Stat(filepath.Join(inst, rel)); err != nil {
			t.Fatalf("%s missing: %v", rel, err)
		}
	}
	env, err := os.ReadFile(filepath.Join(inst, ".env"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(env), "PORT=10001") {
		t.Fatalf("PORT not applied, got:\n%s", env)
	}
	if !strings.Contains(string(env), "BIND_ADDR=0.0.0.0") {
		t.Fatalf("BIND_ADDR not applied, got:\n%s", env)
	}
}

func TestInstanceInitRefusesExisting(t *testing.T) {
	dir, _ := fixture(t)
	c := &InstanceInitCmd{Name: "prod", Dir: dir}
	if err := c.Run(); err != nil {
		t.Fatal(err)
	}
	if err := c.Run(); err == nil {
		t.Fatal("second init on the same name must fail")
	}
}

func TestInstanceComposeCommand(t *testing.T) {
	dir, _ := fixture(t)
	if err := (&InstanceInitCmd{Name: "prod", Dir: dir}).Run(); err != nil {
		t.Fatal(err)
	}

	type call struct {
		args []string
		env  []string
		dir  string
	}
	var got call
	old := composeRunner
	composeRunner = func(args, env []string, wd string) error {
		got = call{args: args, env: env, dir: wd}
		return nil
	}
	t.Cleanup(func() { composeRunner = old })

	if err := (&InstanceUpCmd{Name: "prod", Dir: dir, Build: true}).Run(); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(got.args, " ")
	for _, want := range []string{"compose", "--project-name v2v-prod", "--env-file", "-f", "docker-compose.yml", "up -d --build"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("compose args missing %q: %v", want, got.args)
		}
	}
	if got.dir != dir {
		t.Fatalf("compose cwd = %q, want %q", got.dir, dir)
	}
	wantRoot := "ENV_ROOT=" + filepath.Join(dir, "instances", "prod")
	if !containsString(got.env, wantRoot) {
		t.Fatalf("compose env missing %q", wantRoot)
	}
}

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func takeFixture(t *testing.T) (dir, cfg string, tplRoot string) {
	t.Helper()
	dir, cfg = fixture(t)
	m, _, err := loadManifest(dir)
	if err != nil {
		t.Fatal(err)
	}
	return dir, cfg, filepath.Join(dir, m.TemplateDir)
}

func TestParseTakeSpec(t *testing.T) {
	dir, _, tplRoot := takeFixture(t)
	m, _, err := loadManifest(dir)
	if err != nil {
		t.Fatal(err)
	}
	take, whole, err := parseTakeSpec([]string{"env:PORT", "roles"}, m.Files, tplRoot)
	if err != nil {
		t.Fatal(err)
	}
	if !take["env"]["PORT"] {
		t.Fatalf("env:PORT must parse, got %v", take)
	}
	if !whole["roles"] {
		t.Fatalf("bare roles must mean whole file, got %v", whole)
	}
	for _, spec := range []struct {
		name string
		args []string
	}{
		{"unknown id", []string{"nope:PORT"}},
		{"unknown key", []string{"env:NOPE"}},
		{"trust with key", []string{"trust:10.0.0.0/8"}},
		{"empty key", []string{"env:"}},
	} {
		if _, _, err := parseTakeSpec(spec.args, m.Files, tplRoot); err == nil {
			t.Fatalf("%s must fail closed, args %v", spec.name, spec.args)
		}
	}
}

func TestSyncTakeKeyResetsValue(t *testing.T) {
	dir, cfg := fixture(t)
	writeTree(t, cfg, map[string]string{".env": "PORT=20000\n"})
	s := &ConfigSyncCmd{ConfigCommon: ConfigCommon{Dir: dir, To: cfg, Only: "env", NoPager: true}}
	s.PreferTemplate = []string{"env:PORT"}
	s.Yes = true
	if err := s.Run(); err != nil {
		t.Fatal(err)
	}
	env, err := os.ReadFile(filepath.Join(cfg, ".env"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(env), "PORT=10000") {
		t.Fatalf("taken key must carry template value, got:\n%s", env)
	}
}

func TestSyncTakeRequiresYes(t *testing.T) {
	dir, cfg := fixture(t)
	writeTree(t, cfg, map[string]string{".env": "PORT=20000\n"})
	s := &ConfigSyncCmd{ConfigCommon: ConfigCommon{Dir: dir, To: cfg, Only: "env", NoPager: true}}
	s.PreferTemplate = []string{"env:PORT"}
	if err := s.Run(); err == nil {
		t.Fatal("take without --yes must abort before writing")
	}
	env, err := os.ReadFile(filepath.Join(cfg, ".env"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(env), "PORT=10000") {
		t.Fatalf("must not write before --yes, got:\n%s", env)
	}
	if !strings.Contains(string(env), "PORT=20000") {
		t.Fatalf("local value must survive the aborted run, got:\n%s", env)
	}
}

func TestSyncTakeTrustRefusesEmptyTemplate(t *testing.T) {
	dir, cfg := fixture(t)
	// Fixture cloudflare.txt is comments-only: take must refuse to
	// wipe local ranges rather than empty the file.
	writeTree(t, cfg, map[string]string{"config/trustedproxy/cloudflare.txt": "10.0.0.0/8\n"})
	s := &ConfigSyncCmd{ConfigCommon: ConfigCommon{Dir: dir, To: cfg, Only: "trust", NoPager: true}}
	s.PreferTemplate = []string{"trust"}
	s.Yes = true
	if err := s.Run(); err == nil {
		t.Fatal("take on empty template trust must refuse")
	}
	cur, err := os.ReadFile(filepath.Join(cfg, "config", "trustedproxy", "cloudflare.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(cur), "10.0.0.0/8") {
		t.Fatalf("refused run must leave local file alone, got:\n%s", cur)
	}
}

func TestSyncTakeTrustVerbatim(t *testing.T) {
	dir, cfg := fixture(t)
	writeTree(t, filepath.Join(dir, "template", "server", "config", "trustedproxy"),
		map[string]string{"cloudflare.txt": "# ranges\n198.51.100.0/24\n"})
	writeTree(t, cfg, map[string]string{"config/trustedproxy/cloudflare.txt": "203.0.113.7/32\n"})
	s := &ConfigSyncCmd{ConfigCommon: ConfigCommon{Dir: dir, To: cfg, Only: "trust", NoPager: true}}
	s.PreferTemplate = []string{"trust"}
	s.Yes = true
	if err := s.Run(); err != nil {
		t.Fatal(err)
	}
	cur, err := os.ReadFile(filepath.Join(cfg, "config", "trustedproxy", "cloudflare.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(cur), "198.51.100.0/24") {
		t.Fatalf("taken trust must carry template entries, got:\n%s", cur)
	}
	if strings.Contains(string(cur), "203.0.113.7/32") {
		t.Fatalf("taken trust must drop local entries, got:\n%s", cur)
	}
}

// Regression: a taken key listed in add_policy.env.comment must render
// active (template value), not commented, or the pre-write verify aborts
// with a mismatch.
func TestSyncTakeBeatsEnvCommentPolicy(t *testing.T) {
	dir, cfg := fixture(t)
	writeTree(t, cfg, map[string]string{".env": "INSTANCE_ID=prod-1\n"})
	s := &ConfigSyncCmd{ConfigCommon: ConfigCommon{Dir: dir, To: cfg, Only: "env", NoPager: true}}
	s.PreferTemplate = []string{"env:INSTANCE_ID"}
	s.Yes = true
	if err := s.Run(); err != nil {
		t.Fatal(err)
	}
	env, err := os.ReadFile(filepath.Join(cfg, ".env"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(env), "\nINSTANCE_ID=your-instance-id") {
		t.Fatalf("taken comment-policy key must render active, got:\n%s", env)
	}
	if strings.Contains(string(env), "#INSTANCE_ID=") {
		t.Fatalf("taken key must not stay commented, got:\n%s", env)
	}
}

// Taking a template key that is only a commented default resets the key
// to the template state (disabled): the local active value is dropped
// and the template's commented default stays commented.
func TestSyncTakeCommentedTemplateKeyDisables(t *testing.T) {
	dir, cfg := fixture(t)
	writeTree(t, filepath.Join(dir, "template"), map[string]string{
		".env": "# Port\nPORT=10000\n# Loopback bind\n#BIND_ADDR=127.0.0.1\n",
	})
	writeTree(t, cfg, map[string]string{".env": "PORT=1\nBIND_ADDR=0.0.0.0\n"})
	s := &ConfigSyncCmd{ConfigCommon: ConfigCommon{Dir: dir, To: cfg, Only: "env", NoPager: true}}
	s.PreferTemplate = []string{"env:BIND_ADDR"}
	s.Yes = true
	if err := s.Run(); err != nil {
		t.Fatal(err)
	}
	env, err := os.ReadFile(filepath.Join(cfg, ".env"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(env), "\nBIND_ADDR=0.0.0.0") {
		t.Fatalf("taken local override must be dropped, got:\n%s", env)
	}
	if !strings.Contains(string(env), "#BIND_ADDR=127.0.0.1") {
		t.Fatalf("commented template default must stay commented, got:\n%s", env)
	}
}

func TestSyncTakeBeatsRolesSkip(t *testing.T) {
	dir, cfg := fixture(t)
	writeTree(t, cfg, map[string]string{
		"config/roles.json": "{\"member\": {\"identities\": []}}",
	})
	s := &ConfigSyncCmd{ConfigCommon: ConfigCommon{Dir: dir, To: cfg, Only: "roles", NoPager: true}}
	s.PreferTemplate = []string{"roles:admin"}
	s.Yes = true
	if err := s.Run(); err != nil {
		t.Fatal(err)
	}
	roles, err := os.ReadFile(filepath.Join(cfg, "config", "roles.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(roles), "\"admin\"") {
		t.Fatalf("explicit take must beat add_policy skip, got:\n%s", roles)
	}
	if !strings.Contains(string(roles), "\"member\"") {
		t.Fatalf("local-only role must survive take, got:\n%s", roles)
	}
}

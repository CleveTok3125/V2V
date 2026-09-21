package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTree(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, content := range files {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// fixture builds a manifest-bearing dir plus a separate config root.
func fixture(t *testing.T) (dir, cfg string) {
	t.Helper()
	dir = t.TempDir()
	writeTree(t, filepath.Join(dir, "template"), map[string]string{
		".env":                     "# Port\nPORT=10000\n\n# Instance\nINSTANCE_ID=your-instance-id\n",
		"server/config/roles.json": "{\n    \"admin\": {\"identities\": []}\n}\n",
		"server/config/trustedproxy/cloudflare.txt": "# Cloudflare ranges\n",
		"client/config.jsonc":                       "{\n  // c\n  \"defaults\": {}\n}\n",
	})
	manifest := `{
  "version": 1,
  "type": "v2v-template",
  "templateDir": "template",
  "files": [
    {"id": "env", "path": ".env", "format": "env", "dest": ".env", "keys": ["PORT", "INSTANCE_ID"]},
    {"id": "roles", "path": "server/config/roles.json", "format": "json", "dest": "config/roles.json", "keys": ["admin"]},
    {"id": "trust", "path": "server/config/trustedproxy", "format": "trust-dir", "dest": "config/trustedproxy"},
    {"id": "client", "path": "client/config.jsonc", "format": "jsonc", "target": "client", "dest": "config.jsonc", "keys": ["defaults"]}
  ],
  "add_policy": {
    "env": {"comment": ["INSTANCE_ID"]},
    "roles": {"skip": ["admin"]}
  }
}`
	if err := os.WriteFile(filepath.Join(dir, manifestName), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir, t.TempDir()
}

func TestPathWithin(t *testing.T) {
	base := t.TempDir()
	if !pathWithin(base, base) {
		t.Fatal("same path must count as within")
	}
	if !pathWithin(filepath.Join(base, "a", "b"), base) {
		t.Fatal("descendant must count as within")
	}
	if pathWithin(filepath.Dir(base), base) {
		t.Fatal("parent must not count as within")
	}
	if pathWithin(filepath.Join(base, "..", "sibling"), base) {
		t.Fatal("sibling must not count as within")
	}
}

func TestResolveRefusesTemplateAsTo(t *testing.T) {
	dir, _ := fixture(t)
	if _, err := resolveConfigCtx(ConfigCommon{Dir: dir, To: filepath.Join(dir, "template")}); err == nil {
		t.Fatal("config root inside template must be refused")
	}
}

func TestResolveMissingManifest(t *testing.T) {
	if _, err := resolveConfigCtx(ConfigCommon{Dir: t.TempDir()}); err == nil {
		t.Fatal("missing manifest must fail")
	}
}

func TestSelectFiles(t *testing.T) {
	m := defaultManifest()
	files, err := selectFiles(m, "roles,env")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 {
		t.Fatalf("want 2 files, got %d", len(files))
	}
	if _, err := selectFiles(m, "bogus"); err == nil {
		t.Fatal("unknown id must fail")
	}
	all, err := selectFiles(m, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 {
		t.Fatalf("empty --only must default to env,roles,trust, got %d", len(all))
	}
	for _, f := range all {
		if f.ID == "client" {
			t.Fatal("client must be opt-in, not in the default set")
		}
	}
	if _, err := selectFiles(m, "client"); err != nil {
		t.Fatalf("client must be selectable explicitly: %v", err)
	}
}

func TestSyncEnvCommentPolicyAndRolesSkip(t *testing.T) {
	dir, cfg := fixture(t)
	s := &ConfigSyncCmd{ConfigCommon: ConfigCommon{Dir: dir, To: cfg, Only: "env,roles", NoPager: true}}
	if err := s.Run(); err != nil {
		t.Fatal(err)
	}
	env, err := os.ReadFile(filepath.Join(cfg, ".env"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(env), "#INSTANCE_ID=your-instance-id") {
		t.Fatalf("added placeholder must be commented, got:\n%s", env)
	}
	if strings.Contains(string(env), "\nINSTANCE_ID=") {
		t.Fatalf("added placeholder must not stay active, got:\n%s", env)
	}
	if !strings.Contains(string(env), "PORT=10000") {
		t.Fatalf("kept key must render active, got:\n%s", env)
	}
	roles, err := os.ReadFile(filepath.Join(cfg, "config", "roles.json"))
	if err != nil {
		t.Fatal(err)
	}
	var root map[string]any
	if err := json.Unmarshal(roles, &root); err != nil {
		t.Fatal(err)
	}
	if _, ok := root["admin"]; ok {
		t.Fatalf("skipped role must not be added, got:\n%s", roles)
	}
}

func TestSyncIdempotentSecondRun(t *testing.T) {
	dir, cfg := fixture(t)
	s := &ConfigSyncCmd{ConfigCommon: ConfigCommon{Dir: dir, To: cfg, Only: "env,roles,trust", NoPager: true}}
	if err := s.Run(); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(filepath.Join(cfg, ".env"))
	s2 := &ConfigSyncCmd{ConfigCommon: ConfigCommon{Dir: dir, To: cfg, Only: "env,roles,trust", NoPager: true}}
	if err := s2.Run(); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(filepath.Join(cfg, ".env"))
	if string(before) != string(after) {
		t.Fatalf("second sync must not change output:\n%s\n---\n%s", before, after)
	}
}

func TestSyncPreservesOperatorValue(t *testing.T) {
	dir, cfg := fixture(t)
	writeAtomic(filepath.Join(cfg, ".env"), []byte("PORT=20000\n"))
	s := &ConfigSyncCmd{ConfigCommon: ConfigCommon{Dir: dir, To: cfg, Only: "env", NoPager: true}}
	if err := s.Run(); err != nil {
		t.Fatal(err)
	}
	env, _ := os.ReadFile(filepath.Join(cfg, ".env"))
	if !strings.Contains(string(env), "PORT=20000") {
		t.Fatalf("operator value must win, got:\n%s", env)
	}
}

func TestSyncRefusesWriteIntoTemplate(t *testing.T) {
	dir, _ := fixture(t)
	s := &ConfigSyncCmd{ConfigCommon: ConfigCommon{Dir: dir, To: filepath.Join(dir, "template"), Only: "env", NoPager: true}}
	if err := s.Run(); err == nil {
		t.Fatal("sync into template root must fail")
	}
}

func TestSyncRefusesInvalidLocalJSON(t *testing.T) {
	dir, cfg := fixture(t)
	writeAtomic(filepath.Join(cfg, "config", "roles.json"), []byte("{ this is not json"))
	s := &ConfigSyncCmd{ConfigCommon: ConfigCommon{Dir: dir, To: cfg, Only: "roles", NoPager: true}}
	if err := s.Run(); err == nil {
		t.Fatal("invalid local JSON must fail, not be overwritten")
	}
}

func TestSyncNormalizesConfigModes(t *testing.T) {
	dir, cfg := fixture(t)
	s := &ConfigSyncCmd{ConfigCommon: ConfigCommon{Dir: dir, To: cfg, Only: "env,roles,trust", NoPager: true}}
	if err := s.Run(); err != nil {
		t.Fatal(err)
	}
	assertMode(t, filepath.Join(cfg, ".env"), 0o644)
	assertMode(t, filepath.Join(cfg, "config"), 0o755)
	assertMode(t, filepath.Join(cfg, "config", "roles.json"), 0o644)
	assertMode(t, filepath.Join(cfg, "config", "trustedproxy"), 0o755)
	assertMode(t, filepath.Join(cfg, "config", "trustedproxy", "cloudflare.txt"), 0o644)
}

func assertMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := fi.Mode().Perm(); got != want {
		t.Fatalf("%s mode = %o, want %o", path, got, want)
	}
}

func TestSyncClientRequiresForce(t *testing.T) {
	dir, cfg := fixture(t)
	clientDir := t.TempDir()
	common := ConfigCommon{Dir: dir, To: cfg, Only: "client", ClientDir: clientDir, NoPager: true}
	s := &ConfigSyncCmd{ConfigCommon: common}
	if err := s.Run(); err == nil {
		t.Fatal("client sync without --force must fail")
	}
	if _, err := os.Stat(filepath.Join(clientDir, "config.jsonc")); !os.IsNotExist(err) {
		t.Fatal("client file must not be written without --force")
	}
	s2 := &ConfigSyncCmd{ConfigCommon: common, Force: true}
	if err := s2.Run(); err != nil {
		t.Fatalf("--force must allow the write: %v", err)
	}
	if _, err := os.Stat(filepath.Join(clientDir, "config.jsonc")); err != nil {
		t.Fatalf("client file must be written with --force: %v", err)
	}
}

func TestManifestWriteRefusesBrokenManifest(t *testing.T) {
	dir := t.TempDir()
	broken := []byte(`{ this is not json`)
	path := filepath.Join(dir, manifestName)
	if err := os.WriteFile(path, broken, 0o644); err != nil {
		t.Fatal(err)
	}
	m := &ConfigManifestCmd{Dir: dir, Write: true}
	if err := m.Run(); err == nil {
		t.Fatal("broken manifest must not be silently overwritten")
	}
	after, _ := os.ReadFile(path)
	if string(after) != string(broken) {
		t.Fatalf("broken manifest must be left untouched, got:\n%s", after)
	}
}

func TestCheckDrift(t *testing.T) {
	dir, cfg := fixture(t)
	c := &ConfigCheckCmd{ConfigCommon: ConfigCommon{Dir: dir, To: cfg, Only: "env", NoPager: true}}
	if err := c.Run(); err != nil {
		t.Fatalf("clean manifest must pass: %v", err)
	}
	envPath := filepath.Join(dir, "template", ".env")
	data, _ := os.ReadFile(envPath)
	os.WriteFile(envPath, append(data, []byte("NEW_KEY=1\n")...), 0o600)
	if err := c.Run(); err == nil {
		t.Fatal("drift must fail the check")
	}
}

func TestManifestWriteRefreshesKeysAndKeepsPolicy(t *testing.T) {
	dir, _ := fixture(t)
	envPath := filepath.Join(dir, "template", ".env")
	data, _ := os.ReadFile(envPath)
	os.WriteFile(envPath, append(data, []byte("NEW_KEY=1\n")...), 0o600)

	m := &ConfigManifestCmd{Dir: dir, Write: true}
	if err := m.Run(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, manifestName))
	if err != nil {
		t.Fatal(err)
	}
	var got templateManifest
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.AddPolicy.Env["comment"]) != 1 || got.AddPolicy.Env["comment"][0] != "INSTANCE_ID" {
		t.Fatalf("add_policy must survive regeneration, got %+v", got.AddPolicy)
	}
	found := false
	for _, f := range got.Files {
		if f.ID == "env" {
			found = containsString(f.Keys, "NEW_KEY")
		}
	}
	if !found {
		t.Fatalf("keys must be refreshed, got %+v", got.Files)
	}
}

func TestDstForClientTarget(t *testing.T) {
	dir, _ := fixture(t)
	clientDir := t.TempDir()
	ctx, err := resolveConfigCtx(ConfigCommon{Dir: dir, To: t.TempDir(), ClientDir: clientDir, Only: "client"})
	if err != nil {
		t.Fatal(err)
	}
	dst := ctx.dstFor(ctx.files[0])
	if dst != filepath.Join(clientDir, "config.jsonc") {
		t.Fatalf("client dst wrong: %s", dst)
	}
}

func TestColorizeDiffGate(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	text := "--- a\n+++ b\n@@ -1 +1 @@\n-x\n+y\n same\n"
	if got := colorizeDiff(text); got != text {
		t.Fatalf("NO_COLOR must stay plain, got:\n%q", got)
	}
	for _, line := range []string{"+add", "-del", "@@ -1 +1 @@", "--- a"} {
		if got := paintDiffLine(line); !strings.Contains(got, "\x1b[") {
			t.Fatalf("line %q must carry color, got %q", line, got)
		}
	}
	if got := paintDiffLine("ctx"); got != "ctx" {
		t.Fatalf("context line must stay plain, got %q", got)
	}
}

func TestPagerDecision(t *testing.T) {
	short := "a\nb\n"
	if wantPager(short, true) {
		t.Fatal("short TTY output must not page")
	}
	long := strings.Repeat("line\n", pageThresholdLines+5)
	if !wantPager(long, true) {
		t.Fatal("long TTY output must page")
	}
	if wantPager(long, false) {
		t.Fatal("piped output must never page")
	}
}

func TestPagerCommand(t *testing.T) {
	t.Setenv("PAGER", "")
	name, args, disabled := pagerCommand()
	if disabled || name != "less" || len(args) == 0 {
		t.Fatalf("empty PAGER must default to less: %q %v %v", name, args, disabled)
	}
	t.Setenv("PAGER", "cat")
	if _, _, disabled := pagerCommand(); !disabled {
		t.Fatal("PAGER=cat must disable paging")
	}
	t.Setenv("PAGER", "less -S")
	name, args, disabled = pagerCommand()
	if disabled || name != "less" || len(args) != 1 || args[0] != "-S" {
		t.Fatalf("PAGER args must split: %q %v %v", name, args, disabled)
	}
}

func TestUnifiedFileDiff(t *testing.T) {
	same := fileMerge{Rel: ".env", Current: []byte("A=1\n"), Merged: []byte("A=1\n")}
	if out := unifiedFileDiff(same); out != "" {
		t.Fatalf("identical files must render empty, got:\n%s", out)
	}
	changed := fileMerge{Rel: ".env", Current: []byte("PORT=10000\n"), Merged: []byte("PORT=20000\n")}
	out := unifiedFileDiff(changed)
	for _, want := range []string{"--- config/.env", "+++ merged/.env", "@@", "-PORT=10000", "+PORT=20000"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q, got:\n%s", want, out)
		}
	}
}

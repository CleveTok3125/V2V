package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func takePickFixture(t *testing.T) (dir, cfg string) {
	t.Helper()
	dir, cfg = fixture(t)
	writeTree(t, cfg, map[string]string{
		".env":                               "PORT=20000\n",
		"config/roles.json":                  `{"admin": {"identities": ["x"]}}`,
		"config/trustedproxy/cloudflare.txt": "# mine\n10.0.0.0/8\n",
	})
	return dir, cfg
}

func takePickCtx(t *testing.T, dir, cfg, only string) (*configCtx, []fileMerge) {
	t.Helper()
	common := ConfigCommon{Dir: dir, To: cfg, Only: only, NoPager: true}
	ctx, err := resolveConfigCtx(common)
	if err != nil {
		t.Fatal(err)
	}
	merges, err := mergeAll(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return ctx, merges
}

func TestTakeCandidates(t *testing.T) {
	dir, cfg := takePickFixture(t)
	ctx, merges := takePickCtx(t, dir, cfg, "env,roles,trust")
	cands := takeCandidates(ctx, merges)
	var specs []string
	for _, c := range cands {
		if !c.Preselected {
			t.Fatalf("drift candidate %s must be preselected", c.Spec())
		}
		specs = append(specs, c.Spec())
	}
	want := []string{"env:PORT", "roles:admin", "trust"}
	if strings.Join(specs, ",") != strings.Join(want, ",") {
		t.Fatalf("candidates = %v, want %v", specs, want)
	}
	if cands[0].Local != "20000" || cands[0].Template != "10000" {
		t.Fatalf("env candidate must show both values, got %+v", cands[0])
	}
	take, whole, err := parseTakeSpec(specs, ctx.files, ctx.tplRoot)
	if err != nil {
		t.Fatalf("generated specs must parse: %v", err)
	}
	if !take["env"]["PORT"] || !take["roles"]["admin"] || !whole["trust"] {
		t.Fatalf("parsed take mismatch: %v %v", take, whole)
	}
}

func TestTakeApplyYes(t *testing.T) {
	dir, cfg := takePickFixture(t)
	s := &ConfigTakeCmd{}
	s.Dir, s.To, s.Only, s.NoPager, s.Yes = dir, cfg, "env,roles", true, true
	if err := s.Run(); err != nil {
		t.Fatal(err)
	}
	env, err := os.ReadFile(filepath.Join(cfg, ".env"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(env), "PORT=10000") {
		t.Fatalf("taken key must reset to template, got:\n%s", env)
	}
	roles, err := os.ReadFile(filepath.Join(cfg, "config", "roles.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(roles), `"admin"`) || strings.Contains(string(roles), `"x"`) {
		t.Fatalf("explicit take must beat add_policy skip, got:\n%s", roles)
	}
}

func TestTakeNeedsTTY(t *testing.T) {
	t.Setenv("V2V_NO_TTY", "1")
	dir, cfg := takePickFixture(t)
	s := &ConfigTakeCmd{}
	s.Dir, s.To, s.Only, s.NoPager = dir, cfg, "env", true
	if err := s.Run(); err == nil {
		t.Fatal("non-TTY take without --yes/--list must refuse")
	} else if !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("refusal must point at --yes/--list, got: %v", err)
	}
}

func TestTakeList(t *testing.T) {
	dir, cfg := takePickFixture(t)
	ctx, merges := takePickCtx(t, dir, cfg, "env")
	cands := takeCandidates(ctx, merges)
	text := takeSpecList(cands)
	if strings.TrimSpace(text) != "env:PORT" {
		t.Fatalf("list must print one spec per line, got %q", text)
	}
}

func TestTakeTrustEmptyTemplateRefuses(t *testing.T) {
	dir, cfg := takePickFixture(t)
	s := &ConfigTakeCmd{}
	s.Dir, s.To, s.Only, s.NoPager, s.Yes = dir, cfg, "trust", true, true
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

func TestTakeTrustVerbatim(t *testing.T) {
	dir, cfg := fixture(t)
	writeTree(t, filepath.Join(dir, "template", "server", "config", "trustedproxy"),
		map[string]string{"cloudflare.txt": "# ranges\n198.51.100.0/24\n"})
	writeTree(t, cfg, map[string]string{"config/trustedproxy/cloudflare.txt": "203.0.113.7/32\n"})
	s := &ConfigTakeCmd{}
	s.Dir, s.To, s.Only, s.NoPager, s.Yes = dir, cfg, "trust", true, true
	if err := s.Run(); err != nil {
		t.Fatal(err)
	}
	cur, err := os.ReadFile(filepath.Join(cfg, "config", "trustedproxy", "cloudflare.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(cur), "198.51.100.0/24") || strings.Contains(string(cur), "203.0.113.7/32") {
		t.Fatalf("taken trust must be template-verbatim, got:\n%s", cur)
	}
}

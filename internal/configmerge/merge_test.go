package configmerge

import (
	"strings"
	"testing"
)

func TestRenderEnvPreservesComments(t *testing.T) {
	local := []byte("# Local port docs\nPORT=20000\n# Old cooldown\nMESSAGE_COOLDOWN=200ms\n# Local only\nMY_CUSTOM=1\n")
	incoming := []byte("# New port docs\nPORT=10000\n# New cooldown docs\nMESSAGE_COOLDOWN=500ms\n# New key docs\nMAX_TOTAL_CONNECTIONS=500\n")
	res := OverlayEnv(local, incoming)
	out := string(RenderEnv(res, incoming))
	if !containsLine(out, "# New port docs") {
		t.Fatalf("kept key must carry new template comments, got:\n%s", out)
	}
	if !containsLine(out, "PORT=20000") {
		t.Fatalf("kept key must keep local value, got:\n%s", out)
	}
	if !containsLine(out, "# New key docs") || !containsLine(out, "MAX_TOTAL_CONNECTIONS=500") {
		t.Fatalf("added key must carry new comments and value, got:\n%s", out)
	}
	if !containsLine(out, "# Local only") || !containsLine(out, "MY_CUSTOM=1") {
		t.Fatalf("orphaned key must carry local comments and value, got:\n%s", out)
	}
}

func TestRenderTrustPreservesHeader(t *testing.T) {
	local := []byte("# base header\n10.0.0.0/8\n# office\n203.0.113.7/32\n")
	incoming := []byte("# new header\n# WARNING: header power\n10.0.0.0/8\n198.51.100.0/24\n")
	res := OverlayTrust(local, incoming)
	out := string(RenderTrust(res, incoming))
	if !containsLine(out, "# new header") {
		t.Fatalf("merged trust must carry new header, got:\n%s", out)
	}
	for _, want := range []string{"10.0.0.0/8", "203.0.113.7/32", "198.51.100.0/24"} {
		if !containsLine(out, want) {
			t.Fatalf("missing entry %q, got:\n%s", want, out)
		}
	}
}

func containsLine(out, want string) bool {
	for _, line := range splitLines(out) {
		if line == want {
			return true
		}
	}
	return false
}

func splitLines(s string) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	if start < len(s) {
		out = append(out, s[start:])
	}
	return out
}

func TestOverlayEnvTemplateFirst(t *testing.T) {
	local := []byte("# Local port docs\nPORT=20000\n# Local only\nMY_CUSTOM=1\n")
	incoming := []byte("# New port docs\nPORT=10000\n# New key docs\nMAX_TOTAL_CONNECTIONS=500\n")
	res := OverlayEnv(local, incoming)
	if res.Values["PORT"] != "20000" {
		t.Fatalf("local value must win, got %q", res.Values["PORT"])
	}
	if res.Values["MAX_TOTAL_CONNECTIONS"] != "500" {
		t.Fatalf("new key must be added, got %q", res.Values["MAX_TOTAL_CONNECTIONS"])
	}
	if res.Values["MY_CUSTOM"] != "1" {
		t.Fatalf("orphaned must be kept, got %q", res.Values["MY_CUSTOM"])
	}
	if !contains(res.ChangedUpstream, "PORT") {
		t.Fatalf("PORT differs so must be overridden, got %+v", res.ChangedUpstream)
	}
	out := string(RenderEnv(res, incoming))
	if !containsLine(out, "# New port docs") || !containsLine(out, "PORT=20000") {
		t.Fatalf("kept key must carry new comments with local value, got:\n%s", out)
	}
	if !containsLine(out, "# Local only") {
		t.Fatalf("orphaned must carry local comments, got:\n%s", out)
	}
}

func TestOverlayTrustTemplateFirst(t *testing.T) {
	local := []byte("# local\n10.0.0.0/8\n203.0.113.7/32\n")
	incoming := []byte("# new header\n10.0.0.0/8\n198.51.100.0/24\n")
	res := OverlayTrust(local, incoming)
	if !contains(res.Added, "198.51.100.0/24") || !contains(res.Orphaned, "203.0.113.7/32") {
		t.Fatalf("wrong groups, got %+v", res)
	}
	out := string(RenderTrust(res, incoming))
	if !containsLine(out, "# new header") {
		t.Fatalf("must carry new header, got:\n%s", out)
	}
}

func TestOverlayJSONTemplateFirst(t *testing.T) {
	local := []byte(`{"admin":{"custom_prefix":"LOCAL"},"custom":{"x":1}}`)
	incoming := []byte(`{"admin":{"custom_prefix":"NEW"},"fresh":{"y":2}}`)
	res := OverlayJSON(local, incoming)
	if !contains(res.Added, "fresh") || !contains(res.Overridden, "admin") || !contains(res.Orphaned, "custom") {
		t.Fatalf("wrong groups, got added=%v overridden=%v orphaned=%v", res.Added, res.Overridden, res.Orphaned)
	}
	if res.Values["admin"] == "" || res.Values["fresh"] == "" {
		t.Fatalf("values must resolve, got %+v", res.Values)
	}
}

func TestRenderEnvIdempotent(t *testing.T) {
	tpl := "# ======================== STATIC ========================\n" +
		"# Port the server listens on\n" +
		"PORT=10000\n" +
		"\n" +
		"# Data root (commented default stays commented)\n" +
		"# DATA_DIR=./data\n" +
		"\n" +
		"# Allowed origins\n" +
		"ALLOWED_ORIGINS=http://localhost:8080,https://yourdomain.com\n"
	res := OverlayEnv([]byte(tpl), []byte(tpl))
	if out := RenderEnv(res, []byte(tpl)); string(out) != tpl {
		t.Fatalf("idempotence broken, got:\n%s", out)
	}
}

func TestRenderEnvCommentedActivation(t *testing.T) {
	tpl := "# Data root\n# DATA_DIR=./data\n\n# Port\nPORT=10000\n"
	local := "DATA_DIR=/srv/data\nPORT=10000\n"
	res := OverlayEnv([]byte(local), []byte(tpl))
	out := string(RenderEnv(res, []byte(tpl)))
	want := "# Data root\nDATA_DIR=/srv/data\n\n# Port\nPORT=10000\n"
	if out != want {
		t.Fatalf("activation misplaced, got:\n%s", out)
	}
}

func TestRenderTrustIdempotent(t *testing.T) {
	tpl := "# Trusted ranges.\n#\n# NEVER add office IPs here.\n\n10.0.0.0/8\n\n# Example (stays commented)\n# 203.0.113.7/32\n"
	res := OverlayTrust([]byte(tpl), []byte(tpl))
	if out := RenderTrust(res, []byte(tpl)); string(out) != tpl {
		t.Fatalf("idempotence broken, got:\n%s", out)
	}
}

func TestRenderJSONIdempotent(t *testing.T) {
	tpl := "{\n" +
		"    \"admin\": {\n" +
		"        \"identities\": [\n" +
		"            {\n" +
		"                \"public_key\": \"d619...\",\n" +
		"                \"hmac_shield\": \"a8f2...\",\n" +
		"                \"server_pubkey\": \"abc\"\n" +
		"            }\n" +
		"        ],\n" +
		"        \"can_message_unlimited\": true,\n" +
		"        \"custom_prefix\": \"[Admin] \"\n" +
		"    }\n" +
		"}\n"
	res := OverlayJSON([]byte(tpl), []byte(tpl))
	out, err := RenderJSON(res, []byte(tpl))
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != tpl {
		t.Fatalf("idempotence broken, got:\n%s", out)
	}
}

func TestRenderEnvWithPolicyCommentsAdded(t *testing.T) {
	tpl := "# Port\nPORT=10000\n\n# Instance\nINSTANCE_ID=your-instance-id\n"
	local := "PORT=10000\n"
	res := OverlayEnv([]byte(local), []byte(tpl))
	out := string(RenderEnvWithPolicy(res, []byte(tpl), map[string]bool{"INSTANCE_ID": true}))
	if !containsLine(out, "PORT=10000") {
		t.Fatalf("kept active entry must stay active, got:\n%s", out)
	}
	if !containsLine(out, "#INSTANCE_ID=your-instance-id") {
		t.Fatalf("added key must render commented, got:\n%s", out)
	}
	if containsLine(out, "INSTANCE_ID=your-instance-id") {
		t.Fatalf("added key must not stay active, got:\n%s", out)
	}
}

func TestRenderEnvWithPolicyKeepsLocalCommented(t *testing.T) {
	tpl := "INSTANCE_ID=your-instance-id\n"
	local := "#INSTANCE_ID=operator-choice\n"
	res := OverlayEnv([]byte(local), []byte(tpl))
	out := string(RenderEnvWithPolicy(res, []byte(tpl), map[string]bool{"INSTANCE_ID": true}))
	if !containsLine(out, "#INSTANCE_ID=operator-choice") {
		t.Fatalf("local commented value must win, got:\n%s", out)
	}
}

func TestOverlayJSONWithSkip(t *testing.T) {
	local := []byte(`{"existing":1}`)
	tpl := []byte(`{"existing":1,"admin":{"identities":[]}}`)
	res := OverlayJSONWithSkip(local, tpl, map[string]bool{"admin": true})
	if _, ok := res.Object["admin"]; ok {
		t.Fatalf("admin must be skipped on add, got %+v", res.Object)
	}
	if !contains(res.Added, "existing") == false {
		t.Fatalf("existing present locally must not be added, got %v", res.Added)
	}
	res2 := OverlayJSONWithSkip([]byte(`{"admin":{"identities":[1]}}`), tpl, map[string]bool{"admin": true})
	if _, ok := res2.Object["admin"]; !ok {
		t.Fatalf("operator-defined admin must be kept, got %+v", res2.Object)
	}
}

func TestEnvKeysAndJSONTopKeys(t *testing.T) {
	env := []byte("# c\nB=2\n#A=1\nA=3\n")
	keys := EnvKeys(env)
	if len(keys) != 2 || keys[0] != "B" || keys[1] != "A" {
		t.Fatalf("EnvKeys must return active order, got %v", keys)
	}
	j := []byte("{\n  \"a\": 1,\n  \"b\": 2\n}\n")
	jk := JSONTopKeys(j)
	if len(jk) != 2 || jk[0] != "a" || jk[1] != "b" {
		t.Fatalf("JSONTopKeys must return top-level order, got %v", jk)
	}
}

func TestRenderEnvWithPolicyNilMatchesRenderEnv(t *testing.T) {
	tpl := "# h\nA=1\nB=2\n"
	res := OverlayEnv([]byte("A=9\n"), []byte(tpl))
	if a, b := string(RenderEnv(res, []byte(tpl))), string(RenderEnvWithPolicy(res, []byte(tpl), nil)); a != b {
		t.Fatalf("nil policy must match RenderEnv:\n%q\n%q", a, b)
	}
}

func TestRenderJSONNoHTMLEscape(t *testing.T) {
	tpl := "{\n  \"prompts\": {\n    \"normal\": \"| > & < \"\n  }\n}\n"
	res := OverlayJSON([]byte(tpl), []byte(tpl))
	out, err := RenderJSON(res, []byte(tpl))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), `"| > & < "`) {
		t.Fatalf("HTML chars must stay literal, got:\n%s", out)
	}
	if strings.Contains(string(out), `\u003`) {
		t.Fatalf("output must not HTML-escape, got:\n%s", out)
	}
}

func contains(list []string, key string) bool {
	for _, v := range list {
		if v == key {
			return true
		}
	}
	return false
}

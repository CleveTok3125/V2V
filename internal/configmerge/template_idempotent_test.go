package configmerge

import (
	"os"
	"testing"
)

func TestRealTemplatesIdempotent(t *testing.T) {
	const inst = "../../template/server/instances/default"
	env, err := os.ReadFile(inst + "/.env")
	if err != nil {
		t.Skip("no template")
	}
	if out := RenderEnv(OverlayEnv(env, env), env); string(out) != string(env) {
		t.Fatalf("template .env not idempotent")
	}
	for _, name := range []string{"direct.txt", "cloudflare.txt", "onion.txt"} {
		raw, err := os.ReadFile(inst + "/config/trustedproxy/" + name)
		if err != nil {
			t.Fatal(err)
		}
		if out := RenderTrust(OverlayTrust(raw, raw), raw); string(out) != string(raw) {
			t.Fatalf("%s not idempotent, got:\n%s", name, out)
		}
	}
	roles, err := os.ReadFile(inst + "/config/roles.json")
	if err != nil {
		t.Fatal(err)
	}
	out, err := RenderJSON(OverlayJSON(roles, roles), roles)
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != string(roles) {
		t.Fatalf("roles.json not idempotent, got:\n%s", out)
	}
}

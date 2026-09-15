package env

import (
	"os"
	"testing"
)

// The registry pins every variable name in both directions: keys never
// drift from the strings callers and kong tags use.
func TestRegistry(t *testing.T) {
	cases := []struct {
		key  string
		want string
		fn   func() string
	}{
		{"V2V_TRIPCODE", KeyTripcode, Tripcode},
		{"V2V_PASSPHRASE", KeyPassphrase, Passphrase},
		{"V2V_PROXY", KeyProxy, Proxy},
		{"WEBAUTHN_RPID", KeyWebauthnRPID, WebauthnRPID},
		{"WEBAUTHN_ORIGIN", KeyWebauthnOrigin, WebauthnOrigin},
		{"WEBAUTHN_STORE", KeyWebauthnStore, WebauthnStore},
		{"ALLOWED_ORIGINS", KeyAllowedOrigins, AllowedOrigins},
		{"V2V_CONFIG_DIR", KeyConfigDir, nil},
		{"V2V_CACHE_DIR", KeyCacheDir, nil},
		{"V2V_NO_TTY", KeyNoTTY, nil},
		{"CI", KeyCI, nil},
		{"PROXY_PROVIDER", KeyProxyProvider, ProxyProvider},
		{"TRUSTED_PROXY_DIR", KeyTrustedProxyDir, TrustedProxyDir},
		{"DATA_DIR", KeyDataDir, DataDir},
	}
	for _, c := range cases {
		if c.key != c.want {
			t.Errorf("key constant = %q, want literal %q", c.want, c.key)
		}
		if c.fn == nil {
			continue // flag-bound only: kong resolves, no accessor
		}
		t.Setenv(c.key, "v-"+c.key)
		if got := c.fn(); got != "v-"+c.key {
			t.Errorf("%s() = %q, want value", c.key, got)
		}
	}
	if err := os.Unsetenv(KeyTripcode); err != nil {
		t.Fatal(err)
	}
	if got := Tripcode(); got != "" {
		t.Fatalf("unset Tripcode() = %q, want empty", got)
	}
}

// NoTTY pins the non-interactive override: V2V_NO_TTY accepts
// 1/true/yes case-insensitively, CI=true also forces it, and anything
// else stays interactive.
func TestNoTTY(t *testing.T) {
	for _, v := range []string{"1", "true", "TRUE", "yes", " Yes "} {
		t.Setenv(KeyNoTTY, v)
		t.Setenv(KeyCI, "")
		if !NoTTY() {
			t.Errorf("NoTTY() with %s=%q = false, want true", KeyNoTTY, v)
		}
	}
	for _, v := range []string{"", "0", "no", "false", "maybe"} {
		t.Setenv(KeyNoTTY, v)
		t.Setenv(KeyCI, "")
		if NoTTY() {
			t.Errorf("NoTTY() with %s=%q = true, want false", KeyNoTTY, v)
		}
	}
	t.Setenv(KeyNoTTY, "")
	t.Setenv(KeyCI, "true")
	if !NoTTY() {
		t.Error("NoTTY() with CI=true = false, want true")
	}
}

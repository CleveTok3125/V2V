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

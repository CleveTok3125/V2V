// Package env is the single registry of environment variables the
// repo reads. Adding a variable means adding one Key constant, one
// accessor and one test case; changing read behavior means changing
// one function while the compiler finds every caller.
//
// Two read paths exist by design. Bare runtime lookups go through the
// accessors here. Kong flag tags (V2V_PROXY, V2V_CONFIG_DIR,
// V2V_CACHE_DIR, WEBAUTHN_STORE) keep literal strings because struct
// tags cannot reference constants; their keys are still pinned below
// so the registry lists every variable exactly once.
package env

import (
	"os"
	"strings"
)

// Key names, including flag-bound-only variables that have no
// accessor because kong resolves them.
const (
	KeyTripcode        = "V2V_TRIPCODE"
	KeyPassphrase      = "V2V_PASSPHRASE"
	KeyProxy           = "V2V_PROXY"
	KeyWebauthnRPID    = "WEBAUTHN_RPID"
	KeyWebauthnOrigin  = "WEBAUTHN_ORIGIN"
	KeyWebauthnStore   = "WEBAUTHN_STORE"
	KeyAllowedOrigins  = "ALLOWED_ORIGINS"
	KeyConfigDir       = "V2V_CONFIG_DIR"
	KeyCacheDir        = "V2V_CACHE_DIR"
	KeyDataDir         = "DATA_DIR"
	KeyNoTTY           = "V2V_NO_TTY"
	KeyCI              = "CI"
	KeyProxyProvider   = "PROXY_PROVIDER"
	KeyTrustedProxyDir = "TRUSTED_PROXY_DIR"
	KeyWebtermDir      = "WEBTERM_DIR"
	KeyRoot            = "V2V_ROOT"
)

// Tripcode feeds tripcode entry without prompting (CI).
func Tripcode() string { return os.Getenv(KeyTripcode) }

// Passphrase unlocks sealed key, tripcode and config files.
func Passphrase() string { return os.Getenv(KeyPassphrase) }

// Proxy feeds --proxy without the flag (CI).
func Proxy() string { return os.Getenv(KeyProxy) }

// WebauthnRPID is the relying-party ID fallback for enrollment.
func WebauthnRPID() string { return os.Getenv(KeyWebauthnRPID) }

// WebauthnOrigin is the origin fallback for enrollment and keygen.
func WebauthnOrigin() string { return os.Getenv(KeyWebauthnOrigin) }

// WebauthnStore locates the credential store file.
func WebauthnStore() string { return os.Getenv(KeyWebauthnStore) }

// AllowedOrigins is the raw comma-separated CORS allow-list.
func AllowedOrigins() string { return os.Getenv(KeyAllowedOrigins) }

// DataDir is the server data root for generated files. Empty means
// the caller default (./data); resolution lives caller-side so tests
// can point it anywhere with t.Setenv.
func DataDir() string { return os.Getenv(KeyDataDir) }

// NoTTY forces non-interactive behavior even when a controlling
// terminal exists (e.g. `go test` run from a terminal, where /dev/tty
// opens but no human can answer huh forms). True when V2V_NO_TTY is
// 1/true/yes (case-insensitive) or CI=true. Automation must never hang
// waiting for input, so either signal suffices (OR, fail-closed).
func NoTTY() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(KeyNoTTY))) {
	case "1", "true", "yes":
		return true
	}
	return os.Getenv(KeyCI) == "true"
}

// ProxyProvider is the explicit reverse-proxy chain (e.g.
// "cloudflare,direct"). Empty is fatal at boot: the operator must
// state it, there is no implicit default.
func ProxyProvider() string { return os.Getenv(KeyProxyProvider) }

// TrustedProxyDir holds per-module "<name>.txt" trust files. Empty
// means the caller default (./config/trustedproxy).
func TrustedProxyDir() string { return os.Getenv(KeyTrustedProxyDir) }

// WebtermDir overrides the directory holding the web assets. Empty
// means the caller default (a "webterm" dir next to the executable,
// falling back to ./webterm).
func WebtermDir() string { return os.Getenv(KeyWebtermDir) }

// Root is the instance directory the server/v2vctl operate on. Empty
// means the caller default (instances/default). It must come from the
// process environment (or a flag), never from the instance .env, because
// it is what locates that .env.
func Root() string { return os.Getenv(KeyRoot) }

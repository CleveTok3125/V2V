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

import "os"

// Key names, including flag-bound-only variables that have no
// accessor because kong resolves them.
const (
	KeyTripcode       = "V2V_TRIPCODE"
	KeyPassphrase     = "V2V_PASSPHRASE"
	KeyProxy          = "V2V_PROXY"
	KeyWebauthnRPID   = "WEBAUTHN_RPID"
	KeyWebauthnOrigin = "WEBAUTHN_ORIGIN"
	KeyWebauthnStore  = "WEBAUTHN_STORE"
	KeyAllowedOrigins = "ALLOWED_ORIGINS"
	KeyConfigDir      = "V2V_CONFIG_DIR"
	KeyCacheDir       = "V2V_CACHE_DIR"
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

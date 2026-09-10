package main

import (
	"errors"
	"testing"
)

// Sentinel texts stay byte-identical to the legacy strings so logs
// and grep workflows keep working; errors.Is must resolve each one.
func TestAuthSentinels(t *testing.T) {
	cases := map[error]string{
		ErrEntropyExhaustion:    "auth_error: entropy_exhaustion",
		ErrPayloadTooLarge:      "auth_error: payload_too_large",
		ErrInvalidRoleLength:    "auth_error: invalid_role_length",
		ErrInvalidNonce:         "auth_error: invalid_nonce",
		ErrExpiredNonce:         "auth_error: expired_nonce",
		ErrIPMismatch:           "auth_error: ip_mismatch",
		ErrPasskeyDisabled:      "auth_error: passkey_disabled",
		ErrInvalidRole:          "auth_error: invalid_role",
		ErrVerificationFailed:   "auth_error: verification_failed",
		ErrInvalidSignature:     "auth_error: invalid_signature",
		ErrTripcodeTooLong:      "auth_error: tripcode_too_long",
		ErrCounterNotIncreasing: "counter_not_increasing",
	}
	for err, text := range cases {
		if err.Error() != text {
			t.Errorf("text = %q, want %q", err.Error(), text)
		}
		if !errors.Is(err, err) {
			t.Errorf("errors.Is must resolve %q", text)
		}
		wrapped := errors.Join(errors.New("ctx"), err)
		if !errors.Is(wrapped, err) {
			t.Errorf("errors.Is must unwrap %q", text)
		}
	}
}

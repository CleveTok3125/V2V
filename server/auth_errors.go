package main

import "errors"

// Sentinel auth errors. Messages keep the legacy "auth_error: ..."
// text byte-identical so existing logs and grep workflows do not
// change; call sites wrap with %w so errors.Is works. The handshake
// caller currently discards auth errors, so these are foundation for
// future per-error penalties, not a behavior change today.
var (
	ErrEntropyExhaustion    = errors.New("auth_error: entropy_exhaustion")
	ErrPayloadTooLarge      = errors.New("auth_error: payload_too_large")
	ErrInvalidRoleLength    = errors.New("auth_error: invalid_role_length")
	ErrInvalidNonce         = errors.New("auth_error: invalid_nonce")
	ErrExpiredNonce         = errors.New("auth_error: expired_nonce")
	ErrIPMismatch           = errors.New("auth_error: ip_mismatch")
	ErrPasskeyDisabled      = errors.New("auth_error: passkey_disabled")
	ErrInvalidRole          = errors.New("auth_error: invalid_role")
	ErrVerificationFailed   = errors.New("auth_error: verification_failed")
	ErrInvalidSignature     = errors.New("auth_error: invalid_signature")
	ErrTripcodeTooLong      = errors.New("auth_error: tripcode_too_long")
	ErrCounterNotIncreasing = errors.New("counter_not_increasing")
)

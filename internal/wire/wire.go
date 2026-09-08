// Package wire is the single source of the chat protocol schema: the
// structs below define every message the client and server exchange.
// Both binaries alias these types (type WireMessage = wire.WireMessage)
// instead of redeclaring them: JSON silently drops unknown fields, so
// separate copies lose fields without warning (DisplayName, TmpID/ReplyTo
// and IdentityPub each existed in only some copies). Add fields here,
// never in a local copy.
//
// All optional fields carry omitempty (IdentityPub is never serialized),
// so zero values keep the exact bytes legacy peers expect.
package wire

type Permission struct {
	CanMessageUnlimited bool   `json:"can_message_unlimited"`
	CustomPrefix        string `json:"custom_prefix"`
}

type TripMeta struct {
	Pub         string `json:"pub"`
	Seq         uint32 `json:"seq"`
	Prev        string `json:"prev"`
	Sig         string `json:"sig"`
	ServerPub   string `json:"server_pub"`
	MsgHash     string `json:"msg_hash,omitempty"`
	DisplayName string `json:"display_name,omitempty"`
	TmpID       uint64 `json:"tmp_id,omitempty"`
	ReplyTo     uint64 `json:"reply_to,omitempty"`
}

type WireMessage struct {
	Type        string `json:"type"`
	Time        string `json:"time,omitempty"`
	DisplayName string `json:"displayName,omitempty"`
	// SysKind classifies system lines at the source: "join", "leave" or
	// "date". History replay filters join/leave unless the client asked
	// for them; untagged lines (old disk records) are always sent.
	SysKind string    `json:"sys_kind,omitempty"`
	Text    string    `json:"text,omitempty"`
	Trip    *TripMeta `json:"trip,omitempty"`
	// TmpID is the sender's per-session counter, relayed verbatim and
	// never assigned by the server. ReplyTo quotes a chain height for
	// replies, relayed verbatim and covered by the link (v2+).
	TmpID       uint64 `json:"tmp_id,omitempty"`
	ReplyTo     uint64 `json:"reply_to,omitempty"`
	ChainPrev   string `json:"chain_prev,omitempty"` // hex 64
	ChainHash   string `json:"chain_hash,omitempty"` // hex 64
	ChainHeight uint64 `json:"chain_height,omitempty"`
	ChainVer    int    `json:"chain_ver,omitempty"` // link encoding, current 2
}

type AuthPacket struct {
	Type      string `json:"type"`
	Nonce     string `json:"nonce,omitempty"`
	Role      string `json:"role,omitempty"`
	Signature string `json:"signature,omitempty"`
	Hmac      string `json:"hmac,omitempty"`
	Username  string `json:"username,omitempty"`
	Tripcode  string `json:"tripcode,omitempty"`

	// TripPub is the hex-encoded ed25519 pubkey derived from passphrase.
	// Sent in AuthPacket alongside Tripcode for hashchain sync.
	TripPub string `json:"trip_pub,omitempty"`

	// WebAuthn assertion (all base64url). When present, the nonce is
	// verified as the SHA-256 of the challenge the authenticator signed.
	PasskeyID         string `json:"passkey_id,omitempty"`
	PasskeyAuthData   string `json:"passkey_auth_data,omitempty"`
	PasskeyClientData string `json:"passkey_client_data,omitempty"`
	PasskeySig        string `json:"passkey_sig,omitempty"`

	// Server identity proof (auth_challenge from server)
	ServerPubKey string `json:"server_pubkey,omitempty"`
	ServerSig    string `json:"server_sig,omitempty"`
	ServerHost   string `json:"server_host,omitempty"`

	// Error carries the rejection reason in type=="auth_failed" packets so
	// clients can show why authentication was refused.
	Error string `json:"error,omitempty"`

	// IdentityPub is set server-side on successful ed25519 logins (not
	// serialized) to track concurrent use of the same identity.
	IdentityPub string `json:"-"`

	// AuthType/Perms ride along in auth_success so clients can render
	// /whoami without extra round-trips.
	AuthType string      `json:"auth_type,omitempty"`
	Perms    *Permission `json:"perms,omitempty"`

	// Trip sync fields for hashchain reconnect
	TripSeq  uint32 `json:"trip_seq,omitempty"`
	TripPrev string `json:"trip_prev,omitempty"` // hex 64

	// HistoryJoins asks the server to include join/leave lines in the
	// catch-up replay. Absent means filtered: replay carries chats,
	// date banners and other system lines only.
	HistoryJoins bool `json:"history_joins,omitempty"`
}

// HistorySync is the machine-readable trailer closing a history
// replay, sent after the human footer. Fork-check logic keys off it:
// omitted_hashes lists replay-skipped chain hashes (join/leave), so a
// persisted tip inside that set is filtered-out, not tampered.
type HistorySync struct {
	Type          string   `json:"type"` // "history_sync"
	MinHeight     uint64   `json:"min_height,omitempty"`
	MaxHeight     uint64   `json:"max_height,omitempty"`
	Sent          int      `json:"sent,omitempty"`
	Total         int      `json:"total,omitempty"`
	OmittedHashes []string `json:"omitted_hashes,omitempty"`
	Truncated     bool     `json:"truncated,omitempty"`
}

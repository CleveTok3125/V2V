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
	// Tags classify a line at the source, most general first: a notice
	// carries its leaf tags and the ancestors they imply, e.g. a
	// screening challenge is
	// ["system","system.pow","system.pow.screening"]. The client
	// filters on tags instead of matching message text, and muting a
	// node hides its whole subtree. History replay filters join/leave
	// unless the client asked for them; dates, audits and untagged lines
	// are always sent. Only audit lines chain.
	//
	// There is no backward compatibility with the sys_kind field this
	// replaced, so a history file written by an older build yields
	// untagged notices: its join/leave records replay as ordinary system
	// lines and ignore the client's join filters. Deploy against an
	// empty history.
	Tags []string `json:"tags,omitempty"`
	// SysDate is the calendar day a date banner announces, as
	// "2006-01-02". The banner text alone is a pre-rendered string, so
	// without this the client could only recognise a repeat by comparing
	// the text it had already drawn. Not covered by the chain hash:
	// date notices are unchained, and this is display metadata.
	SysDate string    `json:"sys_date,omitempty"`
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
	// Seq is the server-assigned history cursor: a monotonic counter
	// over every stored line (chained and unchained alike). It orders
	// paging and relay mirroring and is not covered by the chain hash,
	// so it is an ordering aid, never an integrity proof.
	Seq uint64 `json:"seq,omitempty"`
	// SentAt is the server-side send timestamp in RFC3339 with the
	// server's timezone offset, used for human-facing display (/info,
	// the verify page). It is not covered by the chain hash, so it is a
	// display aid, never an integrity proof; wire.Time stays the hashed
	// link input.
	SentAt string `json:"sent_at,omitempty"`
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

	// Platform declares the client runtime ("native" or "web") so the
	// server can offer platform-fitting PoW presets. Absent means
	// native.
	Platform string `json:"platform,omitempty"`
}

// HistorySync is the machine-readable trailer closing a history
// response, sent after the human footer. Fork-check logic keys off its
// window bounds: a persisted tip inside the window but absent from the
// replayed lines means the log changed. MinHeight/MaxHeight cover the
// chained lines the response intended to send; Dropped counts the sends
// a full peer buffer refused, so a trailer with Dropped > 0 describes an
// incomplete window whose holes prove nothing about the log. Direction,
// FirstSeq, LastSeq, NextSeq and More drive the seq-cursor paging used
// by the initial load, /older, refills and relay mirroring.
type HistorySync struct {
	Type      string `json:"type"` // "history_sync"
	MinHeight uint64 `json:"min_height,omitempty"`
	MaxHeight uint64 `json:"max_height,omitempty"`
	Sent      int    `json:"sent,omitempty"`
	Total     int    `json:"total,omitempty"`
	Dropped   int    `json:"dropped,omitempty"`
	Direction string `json:"direction,omitempty"` // "after" or "before"
	FirstSeq  uint64 `json:"first_seq,omitempty"`
	LastSeq   uint64 `json:"last_seq,omitempty"`
	NextSeq   uint64 `json:"next_seq,omitempty"` // resume cursor; 0 when done
	More      bool   `json:"more,omitempty"`
}

// HistoryInfo announces the available history window once after connect,
// so a peer can choose where to start paging without guessing. It
// replaces the connect-time push replay: the peer asks for the slice it
// wants instead of receiving a burst.
type HistoryInfo struct {
	Type      string `json:"type"` // "history_info"
	MinSeq    uint64 `json:"min_seq,omitempty"`
	MaxSeq    uint64 `json:"max_seq,omitempty"`
	MinHeight uint64 `json:"min_height,omitempty"`
	MaxHeight uint64 `json:"max_height,omitempty"`
	Count     int    `json:"count,omitempty"`
}

// HeightRange is one inclusive chained-height interval in a refill
// request: the server returns the chained lines whose chain_height falls
// inside it.
type HeightRange struct {
	From uint64 `json:"from"`
	To   uint64 `json:"to"`
}

// HistoryRequest asks the server for stored history. AfterSeq pages
// ascending (oldest first, seq > AfterSeq), BeforeSeq pages descending
// (newest first, seq < BeforeSeq); 0 means from the oldest / the tip.
// Ranges fetches exact chained heights (refill) and wins over the
// cursors. Before pages older segments (RAM plus disk tiers). The
// response reuses the replay format (lines plus a HistorySync trailer),
// so no new parser is needed.
type HistoryRequest struct {
	Type      string        `json:"type"` // "history_request"
	Before    uint64        `json:"before,omitempty"`
	Limit     int           `json:"limit,omitempty"`
	AfterSeq  *uint64       `json:"after_seq,omitempty"`  // ascending from this seq (0 = oldest)
	BeforeSeq *uint64       `json:"before_seq,omitempty"` // descending from this seq (0 = tip)
	Ranges    []HeightRange `json:"ranges,omitempty"`     // exact chained heights (refill)
}

// PowOffer is a server-issued proof-of-work challenge, signed with the
// server key. The client verifies OfferSig against the pinned server
// pubkey before spending work, then answers with PowResult or
// PowDecline. Tier 0 is never offered (no PoW needed).
type PowOffer struct {
	Type        string `json:"type"` // "pow_offer"
	Tier        int    `json:"tier,omitempty"`
	T           int    `json:"t,omitempty"`
	M           int    `json:"m,omitempty"`
	P           int    `json:"p,omitempty"`
	Difficulty  uint   `json:"difficulty,omitempty"`
	Salt        string `json:"salt,omitempty"`
	Expires     int64  `json:"expires,omitempty"`
	ChallengeID string `json:"challenge_id,omitempty"`
	OfferSig    string `json:"offer_sig,omitempty"`
	ServerPub   string `json:"server_pub,omitempty"`
}

// PowResult carries a solved challenge back to the server.
type PowResult struct {
	Type        string `json:"type"` // "pow_result"
	ChallengeID string `json:"challenge_id,omitempty"`
	Nonce       uint64 `json:"nonce,omitempty"`
}

// PowDecline refuses a challenge (client over local budget). The
// server mutes chat sends until a later challenge passes.
type PowDecline struct {
	Type        string `json:"type"` // "pow_decline"
	ChallengeID string `json:"challenge_id,omitempty"`
}

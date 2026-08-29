package main

import (
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

type Identity struct {
	PublicKey    string `json:"public_key"`
	HmacShield   string `json:"hmac_shield"`
	ServerPubKey string `json:"server_pubkey,omitempty"`
}

// PasskeyIdentity is a WebAuthn credential accepted for a role. Only public
// material lives here: the private key never leaves the user's authenticator,
// mirroring how Identity holds just a public key.
type PasskeyIdentity struct {
	CredentialID string `json:"credential_id"` // base64url of the credential ID
	PublicKey    string `json:"public_key"`    // COSE_Key CBOR, base64url
	AddedAt      string `json:"added_at,omitempty"`
}

type Permission struct {
	CanMessageUnlimited bool   `json:"can_message_unlimited"`
	CustomPrefix        string `json:"custom_prefix"`
}

type RoleDefinition struct {
	Identities []Identity        `json:"identities"`
	Passkeys   []PasskeyIdentity `json:"passkeys,omitempty"`
	Permission
}

type ClientSession struct {
	Conn        *websocket.Conn
	DisplayName string
	Tripcode    string
	Perms       Permission
	Send        chan []byte
	// IdentityPub pins the ed25519 identity (pubkey hex) behind this
	// session; empty for guests and web passkey sessions.
	IdentityPub string
	TripPub     string
	TripBadge   string
	Host        string // Host header at handshake, for https trip link generation
}

type TripChain struct {
	Seq      uint32
	PrevHash []byte // 32 bytes
	LastHash []byte // msgHash of last message for debugging
}

type TripMeta struct {
	Pub         string `json:"pub"`
	Seq         uint32 `json:"seq"`
	Prev        string `json:"prev"`
	Sig         string `json:"sig"`
	ServerPub   string `json:"server_pub"`
	MsgHash     string `json:"msg_hash,omitempty"`
	DisplayName string `json:"display_name,omitempty"`
}

type WireMessage struct {
	Type        string    `json:"type"` // "chat" or "system"
	Time        string    `json:"time,omitempty"`
	DisplayName string    `json:"displayName,omitempty"`
	Text        string    `json:"text,omitempty"`
	Trip        *TripMeta `json:"trip,omitempty"`
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
}

type ServerIdentity struct {
	PublicKey  string `json:"public_key"`
	PrivateKey string `json:"private_key"`
}

type NonceMeta struct {
	ExpiresAt time.Time
	IP        string
}

type RateLimitRecord struct {
	FailCount  int
	UnlockTime time.Time
}

type ChatServer struct {
	StartTime time.Time

	Clients   map[*websocket.Conn]*ClientSession
	ClientsMu sync.RWMutex

	IpCounts   map[string]int
	IpCountsMu sync.Mutex

	LastConnectTime map[string]time.Time
	LastConnectMu   sync.Mutex

	AuthFails   map[string]RateLimitRecord
	AuthFailsMu sync.Mutex

	ChatHistory     []string
	ChatHistorySize int
	HistoryMu       sync.RWMutex
	HistoryStore    *HistoryStore

	LastMessageDate   string
	LastMessageDateMu sync.Mutex

	ActiveNonces sync.Map
	Upgrader     websocket.Upgrader

	// ActiveIdentities tracks the live session holding each privileged
	// ed25519 identity (pubkey hex -> session) for concurrency alerts.
	ActiveIdentities sync.Map

	TripChains sync.Map // pub hex -> TripChain

	TripVerifyLast   map[string]time.Time
	TripVerifyLastMu sync.Mutex

	WebAuthn *WebAuthnStore

	RoleRegistry   map[string]RoleDefinition
	RoleRegistryMu sync.RWMutex

	ServerID *ServerIdentity
}

func NewChatServer() *ChatServer {
	return &ChatServer{
		StartTime:       time.Now(),
		Clients:          make(map[*websocket.Conn]*ClientSession),
		IpCounts:         make(map[string]int),
		LastConnectTime:  make(map[string]time.Time),
		AuthFails:        make(map[string]RateLimitRecord),
		TripVerifyLast:   make(map[string]time.Time),
		ChatHistory:      make([]string, 0),
		RoleRegistry:     make(map[string]RoleDefinition),
		WebAuthn:        NewWebAuthnStore(os.Getenv("WEBAUTHN_STORE")),
		Upgrader: websocket.Upgrader{
			ReadBufferSize:  1024,
			WriteBufferSize: 1024,
			CheckOrigin: func(r *http.Request) bool {
				origin := r.Header.Get("Origin")

				if origin == "" {
					return true
				}

				for _, o := range Cfg.Static.AllowedOrigins {
					if origin == strings.TrimSpace(o) {
						return true
					}
				}

				log.Printf("⛔ [SECURITY] Chặn kết nối từ Origin không hợp lệ: %s", origin)
				return false
			},
		},
	}
}

func GetDefaultPermission() Permission {
	return Permission{
		CanMessageUnlimited: false,
		CustomPrefix:        "",
	}
}

package main

import (
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

type Identity struct {
	PublicKey  string `json:"public_key"`
	HmacShield string `json:"hmac_shield"`
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
}

type AuthPacket struct {
	Type      string `json:"type"`
	Nonce     string `json:"nonce,omitempty"`
	Role      string `json:"role,omitempty"`
	Signature string `json:"signature,omitempty"`
	Hmac      string `json:"hmac,omitempty"`
	Username  string `json:"username,omitempty"`
	Tripcode  string `json:"tripcode,omitempty"`

	// WebAuthn assertion (all base64url). When present, the nonce is
	// verified as the SHA-256 of the challenge the authenticator signed.
	PasskeyID         string `json:"passkey_id,omitempty"`
	PasskeyAuthData   string `json:"passkey_auth_data,omitempty"`
	PasskeyClientData string `json:"passkey_client_data,omitempty"`
	PasskeySig        string `json:"passkey_sig,omitempty"`

	// Error carries the rejection reason in type=="auth_failed" packets so
	// clients can show why authentication was refused.
	Error string `json:"error,omitempty"`

	// IdentityPub is set server-side on successful ed25519 logins (not
	// serialized) to track concurrent use of the same identity.
	IdentityPub string `json:"-"`
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

	WebAuthn *WebAuthnStore

	RoleRegistry   map[string]RoleDefinition
	RoleRegistryMu sync.RWMutex
}

func NewChatServer() *ChatServer {
	return &ChatServer{
		StartTime:       time.Now(),
		Clients:         make(map[*websocket.Conn]*ClientSession),
		IpCounts:        make(map[string]int),
		LastConnectTime: make(map[string]time.Time),
		AuthFails:       make(map[string]RateLimitRecord),
		ChatHistory:     make([]string, 0),
		RoleRegistry:    make(map[string]RoleDefinition),
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

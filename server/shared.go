package main

import (
	"crypto/rand"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/CleveTok3125/V2V/internal/env"
	"github.com/CleveTok3125/V2V/internal/guard"
	"github.com/CleveTok3125/V2V/internal/wire"
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

type Permission = wire.Permission

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
	// WantJoins asks for join/leave lines in the catch-up replay.
	// Live broadcasts always carry them; only replay filters.
	WantJoins bool
}

type TripChain struct {
	Seq      uint32
	PrevHash []byte // 32 bytes
	LastHash []byte // msgHash of last message for debugging
}

// Protocol schema lives in internal/wire (single source). Aliases keep
// every existing reference compiling while guaranteeing client and
// server serialize identically.
type (
	TripMeta    = wire.TripMeta
	WireMessage = wire.WireMessage
	AuthPacket  = wire.AuthPacket
	HistorySync = wire.HistorySync
)

type ServerIdentity struct {
	PublicKey  string `json:"public_key"`
	PrivateKey string `json:"private_key"`
}

type NonceMeta struct {
	ExpiresAt time.Time
	IP        string
}

// RateLimitRecord is the guard penalty record. Aliased (not copied) so
// auth paths never convert between two identical structs.
type RateLimitRecord = guard.RateLimitRecord

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

	// BroadcastMu serializes link+send so every client receives messages
	// in chain order (see server/chain.go). Leaf locks inside: HistoryMu,
	// then ClientsMu.
	BroadcastMu sync.Mutex

	// Global message chain tip. Guarded by HistoryMu; set by initChainLocked.
	chainTip    [32]byte
	chainHeight uint64
	chainReady  bool

	LastMessageDate   string
	LastMessageDateMu sync.Mutex

	ActiveNonces sync.Map
	Upgrader     websocket.Upgrader

	// ActiveIdentities tracks the live session holding each privileged
	// ed25519 identity (pubkey hex -> session) for concurrency alerts.
	ActiveIdentities sync.Map

	TripChains   sync.Map // pub hex -> TripChain
	TripChainsMu sync.Mutex

	// Display identity salt per server session (ephemeral, not persisted)
	DisplaySalt []byte

	// Active display names counter for serial handling (Alice#a1b2 -> Alice#a1b2-2)
	DisplayNameCount   map[string]int
	DisplayNameCountMu sync.Mutex

	WebAuthn *WebAuthnStore

	RoleRegistry   map[string]RoleDefinition
	RoleRegistryMu sync.RWMutex

	ServerID *ServerIdentity
}

func NewChatServer() *ChatServer {
	salt := make([]byte, 32)
	if _, err := rand.Read(salt); err != nil {
		// fallback to time-based if rand fails (should not happen)
		salt = []byte(time.Now().String())
	}
	return &ChatServer{
		StartTime:        time.Now(),
		Clients:           make(map[*websocket.Conn]*ClientSession),
		IpCounts:          make(map[string]int),
		LastConnectTime:   make(map[string]time.Time),
		AuthFails:         make(map[string]RateLimitRecord),
		DisplaySalt:       salt,
		DisplayNameCount:  make(map[string]int),
		ChatHistory:       make([]string, 0),
		RoleRegistry:      make(map[string]RoleDefinition),
		WebAuthn:         NewWebAuthnStore(env.WebauthnStore()),
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

				logWarnf("⛔ [SECURITY] Chặn kết nối từ Origin không hợp lệ: %s", origin)
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

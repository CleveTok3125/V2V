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

type Permission = wire.Permission

type RoleDefinition struct {
	Identities []Identity `json:"identities"`
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
	// LastSegmentTime throttles on-demand history requests per
	// session. Only ReadPump touches it (single goroutine), so no
	// lock is needed.
	LastSegmentTime time.Time
}

type TripChain struct {
	Seq      uint32
	PrevHash []byte // 32 bytes
	LastHash []byte // msgHash of last message for debugging
	// LastSeen is the last time this trip chain advanced. Used to bound
	// the map: guest keys are cheap to mint, so an unbounded set would be
	// a memory amplification vector.
	LastSeen time.Time
}

// Protocol schema lives in internal/wire (single source). Aliases keep
// every existing reference compiling while guaranteeing client and
// server serialize identically.
type (
	TripMeta       = wire.TripMeta
	WireMessage    = wire.WireMessage
	AuthPacket     = wire.AuthPacket
	HistorySync    = wire.HistorySync
	HistoryRequest = wire.HistoryRequest
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

// ChainService owns the message chain tip, the in-memory history and
// the on-disk store. Mu guards tip, height, ready, History and
// HistorySize together so linkAndStore stays atomic across both.
type ChainService struct {
	Mu          sync.RWMutex
	tip         [32]byte
	height      uint64
	ready       bool
	History     []string
	HistorySize int
	Store       *HistoryStore
}

// Hub owns presence and send ordering: the client set, the identity
// slots, the display-name serials and the date marker, plus BroadcastMu
// which serializes link+send so every client receives messages in chain
// order (see server/chain.go). chain is a one-way back-ref; ChainService
// never points back.
//
// Lock order: BroadcastMu -> LastMessageDateMu -> HistoryMu (Chain.Mu)
// -> ClientsMu.
type Hub struct {
	// BroadcastMu serializes link+send so every client receives messages
	// in chain order. Leaf locks inside: HistoryMu (Chain.Mu), then
	// ClientsMu.
	BroadcastMu sync.Mutex

	Clients   map[*websocket.Conn]*ClientSession
	ClientsMu sync.RWMutex

	// ActiveIdentities tracks the live session holding each privileged
	// ed25519 identity (pubkey hex -> session) for concurrency alerts.
	ActiveIdentities sync.Map

	// Active display names counter for serial handling (Alice#a1b2 -> Alice#a1b2-2)
	DisplayNameCount   map[string]int
	DisplayNameCountMu sync.Mutex

	LastMessageDate   string
	LastMessageDateMu sync.Mutex

	chain *ChainService
}

type ChatServer struct {
	StartTime time.Time

	Hub Hub

	IpCounts   map[string]int
	IpCountsMu sync.Mutex

	LastConnectTime map[string]time.Time
	LastConnectMu   sync.Mutex

	AuthFails   map[string]RateLimitRecord
	AuthFailsMu sync.Mutex

	Chain ChainService

	ActiveNonces sync.Map
	Upgrader     websocket.Upgrader

	TripChains   sync.Map // pub hex -> TripChain
	TripChainsMu sync.Mutex

	// Display identity salt per server session (ephemeral, not persisted)
	DisplaySalt []byte

	WebAuthn *WebAuthnStore

	RoleRegistry   map[string]RoleDefinition
	RoleRegistryMu sync.RWMutex

	ServerID *ServerIdentity
}

func NewChatServer() *ChatServer {
	salt := make([]byte, 32)
	if _, err := rand.Read(salt); err != nil {
		// The salt protects display-name anonymity; a predictable
		// fallback must never be silent.
		logWarnf("⚠️ [SECURITY] crypto/rand thất bại khi sinh DisplaySalt (%v); dùng fallback time-based — hash định danh tên hiển thị trở nên dự đoán được, có thể bị đối chiếu ngược", err)
		salt = []byte(time.Now().String())
	}
	s := &ChatServer{
		StartTime:       time.Now(),
		Hub:             Hub{Clients: make(map[*websocket.Conn]*ClientSession), DisplayNameCount: make(map[string]int)},
		IpCounts:        make(map[string]int),
		LastConnectTime: make(map[string]time.Time),
		AuthFails:       make(map[string]RateLimitRecord),
		DisplaySalt:     salt,
		Chain:           ChainService{History: make([]string, 0)},
		RoleRegistry:    make(map[string]RoleDefinition),
		WebAuthn:        NewWebAuthnStore(env.WebauthnStore()),
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
	s.Hub.chain = &s.Chain
	return s
}

func GetDefaultPermission() Permission {
	return Permission{
		CanMessageUnlimited: false,
		CustomPrefix:        "",
	}
}

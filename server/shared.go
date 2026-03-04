package main

import (
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

type AppConfig struct {
	AllowedOrigins      []string
	MaxConnectionsPerIP int
	MaxMessageLength    int
	MaxMessageLine      int
	MessageCooldown     time.Duration
	MaxHistoryBytes     int
	MaxHistorySend      int
	MaxUsernameLength   int
	ConnectionCooldown  time.Duration
	Port                string
	StatusURL           string
	DownloadURL         string
	HomepageURL         string
	InstanceID          string
	Timezone            *time.Location
}

type Identity struct {
	PublicKey  string `json:"public_key"`
	HmacShield string `json:"hmac_shield"`
}

type Permission struct {
	CanMessageUnlimited bool   `json:"can_message_unlimited"`
	CustomPrefix        string `json:"custom_prefix"`
}

type RoleDefinition struct {
	Identities []Identity `json:"identities"`
	Permission
}

type ClientSession struct {
	Conn        *websocket.Conn
	DisplayName string
	Perms       Permission
}

type AuthPacket struct {
	Type      string `json:"type"`
	Nonce     string `json:"nonce,omitempty"`
	Role      string `json:"role,omitempty"`
	Signature string `json:"signature,omitempty"`
	Hmac      string `json:"hmac,omitempty"`
	Username  string `json:"username,omitempty"`
}

type NonceMeta struct {
	ExpiresAt time.Time
	IP        string
}

type RateLimitRecord struct {
	FailCount  int
	UnlockTime time.Time
}

var Cfg AppConfig

// need define serverstate struct in the future
var (
	ServerStartTime = time.Now()

	Clients   = make(map[*websocket.Conn]*ClientSession)
	ClientsMu sync.Mutex

	IpCounts   = make(map[string]int)
	IpCountsMu sync.Mutex

	LastConnectTime = make(map[string]time.Time)
	LastConnectMu   sync.Mutex

	ChatHistory     []string
	ChatHistorySize int
	HistoryMu       sync.RWMutex

	LastMessageDate   string
	LastMessageDateMu sync.Mutex

	Upgrader = websocket.Upgrader{
		ReadBufferSize:  1024,
		WriteBufferSize: 1024,
		CheckOrigin: func(r *http.Request) bool {
			origin := r.Header.Get("Origin")

			if origin == "" {
				return true
			}

			for _, o := range Cfg.AllowedOrigins {
				if origin == strings.TrimSpace(o) {
					return true
				}
			}

			log.Printf("⛔ [SECURITY] Chặn kết nối từ Origin không hợp lệ: %s", origin)
			return false
		},
	}

	RoleRegistry = make(map[string]RoleDefinition)

	ActiveNonces sync.Map
	AuthFails    = make(map[string]RateLimitRecord)
	AuthFailsMu  sync.Mutex
)

func GetDefaultPermission() Permission {
	return Permission{
		CanMessageUnlimited: false,
		CustomPrefix:        "",
	}
}

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
	Tripcode    string
	Perms       Permission
	Send        chan []byte
}

type AuthPacket struct {
	Type      string `json:"type"`
	Nonce     string `json:"nonce,omitempty"`
	Role      string `json:"role,omitempty"`
	Signature string `json:"signature,omitempty"`
	Hmac      string `json:"hmac,omitempty"`
	Username  string `json:"username,omitempty"`
	Tripcode  string `json:"tripcode,omitempty"`
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

	LastMessageDate   string
	LastMessageDateMu sync.Mutex

	ActiveNonces sync.Map
	Upgrader     websocket.Upgrader

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

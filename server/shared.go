package main

import (
	"crypto/rand"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/CleveTok3125/V2V/lib/env"
	"github.com/CleveTok3125/V2V/lib/guard"
	"github.com/CleveTok3125/V2V/lib/wire"
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
	// IP is the resolved client address owning this session.
	IP string
	// AuthType names how the session authenticated
	// ("guest", "ed25519", "passkey").
	AuthType string
	// Platform is the client platform ("native" default, "web" when
	// the client declares it): PoW offers use Threads=1 for web.
	Platform string
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
	// LastSeen is the last time this trip chain advanced. Used to bound
	// the map: guest keys are cheap to mint, so an unbounded set would be
	// a memory amplification vector.
	LastSeen time.Time
}

// VoteScore is the cumulative total for one chain height. Stored by
// height only: no voter identity is kept long-term.
type VoteScore struct {
	Upvotes   int
	Downvotes int
}

// voteDedupEntry remembers one IP's last direction for one height so a
// repeat reads as duplicate. Ephemeral: expires after voteDedupTTL
// and never persists.
type voteDedupEntry struct {
	Direction int8
	At        time.Time
}

// voteDedupTTL bounds the duplicate memory: past it a repeat counts as
// a new vote. 24h covers brigade retries without growing forever.
const voteDedupTTL = 24 * time.Hour

// Protocol schema lives in lib/wire (single source). Aliases keep
// every existing reference compiling while guaranteeing client and
// server serialize identically.
type (
	TripMeta       = wire.TripMeta
	WireMessage    = wire.WireMessage
	AuthPacket     = wire.AuthPacket
	HistorySync    = wire.HistorySync
	HistoryRequest = wire.HistoryRequest
	HeightRange    = wire.HeightRange
	HistoryInfo    = wire.HistoryInfo
	VoteRequest    = wire.VoteRequest
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
	Mu     sync.RWMutex
	tip    [32]byte
	height uint64
	ready  bool
	// seq is the last history sequence assigned. Every stored line
	// (chained or not) gets one, monotonically, in storage order; it is
	// the paging/relay cursor and is not part of the chain hash.
	seq         uint64
	History     []string
	HistorySize int
	Store       *HistoryStore
}

// Hub owns presence and send ordering: the client set, identity slots,
// display-name serials and the date marker, plus BroadcastMu which
// serializes link+send so every client receives messages in chain order.
// Lock order: BroadcastMu -> LastMessageDateMu -> HistoryMu (Chain.Mu)
// -> ClientsMu.
type Hub struct {
	// BroadcastMu serializes link+send so every client receives messages
	// in chain order. Leaf locks inside: HistoryMu (Chain.Mu), then
	// ClientsMu.
	BroadcastMu sync.Mutex

	Clients   map[*websocket.Conn]*ClientSession
	ClientsMu sync.RWMutex

	// drops counts frames shed on a full send queue (fanout, echo,
	// history-info, concurrent-identity alert). Cumulative like
	// RejectStat; the disk queue keeps its own counter in HistoryStore.
	// int64 plus atomic: no new lock on the broadcast path.
	drops int64

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

	// HistoryBudget throttles history requests per IP by cost (lines
	// returned), so two connections from one IP share one budget and an
	// expensive disk read costs more than a RAM page.
	HistoryBudget *guard.BudgetMap

	// SlowCooldown throttles chat per IP at tier 2+: base cooldown
	// times the tier multiplier. Per-IP (not per-session) so opening
	// another connection cannot halve it.
	SlowCooldown *guard.CooldownMap

	// Blocklist is the operator IP/CIDR denylist matched before any
	// rate limit. Inflight counts handshakes between the global-cap
	// check and registration, so a burst cannot overshoot the cap.
	Blocklist []*net.IPNet
	Inflight  int64

	// Rejects tallies cumulative admission refusals by cause for the
	// under-attack transition log. No endpoint exposes it: the operator
	// reads one line per incident, not a dashboard.
	Rejects RejectStat

	LastConnectTime map[string]time.Time
	LastConnectMu   sync.Mutex

	AuthFails   map[string]RateLimitRecord
	AuthFailsMu sync.Mutex

	Chain ChainService

	ActiveNonces sync.Map
	Upgrader     websocket.Upgrader

	// PairTickets holds outstanding desktop passkey pairings, keyed by the
	// ticket the browser ceremony signs. A ticket costs only memory: waiting
	// for the human happens over poll requests, not on a held socket, so the
	// store is bounded by pairMaxPending rather than by connection slots.
	// See pair.go.
	PairTickets   map[string]*pairTicket
	PairTicketsMu sync.Mutex

	TripChains   sync.Map // pub hex -> TripChain
	TripChainsMu sync.Mutex

	// VoteScores holds cumulative vote totals by chain height. No voter
	// identity is kept: one height maps to one score. VoteDedup is the
	// short-term duplicate memory (IP+height -> last dir), expiring
	// after voteDedupTTL. VoteCooldown throttles vote rate per IP at the
	// chat message cadence. All three share VoteMu.
	VoteScores   map[uint64]*VoteScore
	VoteDedup    map[string]voteDedupEntry
	VoteCooldown *guard.CooldownMap
	VoteMu       sync.Mutex

	// Display identity salt per server session (ephemeral, not persisted)
	DisplaySalt []byte

	WebAuthn *WebAuthnStore

	// Gate issues PoW challenges and lease passes; Attack owns the
	// under-attack flag and pressure signals.
	Gate   *GateStore
	Attack *AttackState

	// Behavior scores IPs from metadata and hands out in-chat PoW
	// challenges through Screener. Nil disables both legs.
	Behavior *BehaviorEngine
	Screener *PowScreener

	RoleRegistry   map[string]RoleDefinition
	RoleRegistryMu sync.RWMutex

	ServerID *ServerIdentity
}

func NewChatServer() *ChatServer {
	salt := make([]byte, 32)
	if _, err := rand.Read(salt); err != nil {
		// The salt protects display-name anonymity; a predictable
		// fallback must never be silent.
		logWarnf("⚠️ (security,fail) crypto/rand thất bại khi sinh DisplaySalt (%v); dùng fallback time-based — hash định danh tên hiển thị trở nên dự đoán được, có thể bị đối chiếu ngược", err)
		salt = []byte(time.Now().String())
	}
	s := &ChatServer{
		StartTime:       time.Now(),
		Hub:             Hub{Clients: make(map[*websocket.Conn]*ClientSession), DisplayNameCount: make(map[string]int)},
		IpCounts:        make(map[string]int),
		HistoryBudget:   guard.NewBudgetMap(),
		SlowCooldown:    guard.NewCooldownMap(),
		LastConnectTime: make(map[string]time.Time),
		AuthFails:       make(map[string]RateLimitRecord),
		DisplaySalt:     salt,
		Chain:           ChainService{History: make([]string, 0)},
		RoleRegistry:    make(map[string]RoleDefinition),
		PairTickets:     make(map[string]*pairTicket),
		VoteScores:      make(map[uint64]*VoteScore),
		VoteDedup:       make(map[string]voteDedupEntry),
		VoteCooldown:    guard.NewCooldownMap(),
		WebAuthn:        NewWebAuthnStore(env.WebauthnStore()),
		Gate:            NewGateStore(),
		Attack:          NewAttackState(),
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

				logWarnf("⛔ (security,reject) Chặn kết nối từ Origin không hợp lệ: %s", origin)
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

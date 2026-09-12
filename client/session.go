package main

import (
	"crypto/ed25519"
	"io"
	"sync"
	"time"
)

// Session carries the chat session state shared by the read pump, the
// input loop, the render path and the chain tracker.
type Session struct {
	// Connection and identity.
	Conn      wsConn
	WSURL     string
	Username  string
	Challenge AuthPacket
	TripPriv  ed25519.PrivateKey
	TripPub   ed25519.PublicKey
	TripBadge string
	TripSeq   uint32
	TripPrev  []byte
	AuthType  string
	Role      string
	Unlimited bool
	Prefix    string
	Connected time.Time

	// Terminal and output funnel.
	Term     inputTerminal
	Out      io.Writer
	Quitting chan bool

	// Display toggles and locks.
	ShowJoinLeave bool
	ShowJoinMu    sync.RWMutex
	ShowMeta      bool
	ShowMetaMu    sync.RWMutex
	AutoVerify    bool
	AutoVerifyMu  sync.RWMutex
	DisplayMu     sync.Mutex
	ActiveTab     int
	TabChat       *tabBuffer
	TabSys        *tabBuffer
	PrintGen      uint64

	// Coalesced repaint state.
	RefreshMu      sync.Mutex
	LastRefresh    time.Time
	RefreshPending bool

	// Async verify worker.
	VerifyCh        chan verifyJob
	VerifyMu        sync.Mutex
	VerifyCloseOnce sync.Once

	// Outgoing message state.
	TmpSeq          uint64
	PendingReplyTo  uint64
	ReplyDraft      uint64
	LastMessageTime time.Time

	// Pending placeholders and early echoes.
	PendingPlaceholders []pendingMsg
	PendingEchoes       []pendingEcho

	// Chain tracking and fork detection.
	ChainTip         [32]byte
	ChainHeight      uint64
	ChainHaveTip     bool
	ChainWarned      bool
	ServerPubHex     string
	TipPath          string
	PersistedTip     [32]byte
	PersistedHeight  uint64
	PersistedServer  string
	HavePersistedTip bool
	InSync           bool
	SyncHashes       map[string]bool
	TipSinceSave     uint64
	WireIdx          *wireIndex
	RenderCache      *renderCache

	// Read-pump banner staging.
	PendingDateBanner     string
	PendingDateBannerWire *WireMessage
}

// NewSession returns a Session with the channels, maps and buffers that
// main() previously created alongside the first use.
func NewSession() *Session {
	return &Session{
		Quitting:            make(chan bool, 1),
		VerifyCh:            make(chan verifyJob, 128),
		TripPrev:            make([]byte, 32),
		PendingPlaceholders: []pendingMsg{},
		SyncHashes:          map[string]bool{},
	}
}

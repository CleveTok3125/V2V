package main

import (
	"crypto/ed25519"
	"crypto/hmac"
	cryptorand "crypto/rand"
	"crypto/sha512"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"math/rand/v2"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/CleveTok3125/V2V/internal/strutil"
)

// Session carries the chat session state shared by the read pump, the
// input loop, the render path and the chain tracker. Sub-state lives
// in the Display, Chain, Verify and Pending groups; the root keeps
// only identity and connection lifecycle.
//
// Lock order is fixed Display -> Chain -> Pending: group locks nest
// only in that direction, never the reverse. DisplayMu stays the
// god-lock for erase math (tabs + PrintGen + pending must stay
// atomic); ChainMu serializes tip updates between the pump and the
// input loop; PendingMu guards the placeholder/echo queues at the
// two threads that share them. Pump-side chain reads (InSync,
// SyncHashes, HaveTip in checkChainLink and handleHistorySync) stay
// under caller-held DisplayMu with no ChainMu: the pump is their
// only writer, so a second lock would add nothing.
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

	Quitting chan bool
	// PumpDone closes when runPump returns. gracefulQuit waits on it
	// (bounded) so the goodbye flush and pump teardown complete
	// instead of racing a fixed sleep. Nil for test-built sessions
	// that never start a pump: gracefulQuit skips the wait then.
	PumpDone chan struct{}

	Display DisplayState
	Chain   ChainState
	Verify  VerifyState
	Pending PendingState
}

// DisplayState is everything the render path paints: tabs, buffers,
// generation counters, toggles, terminal and output funnel.
type DisplayState struct {
	// Display toggles and locks.
	ShowJoinLeave bool
	ShowJoinMu    sync.RWMutex
	ShowMeta      bool
	ShowMetaMu    sync.RWMutex
	DisplayMu     sync.Mutex
	ActiveTab     int
	TabChat       *tabBuffer
	TabSys        *tabBuffer
	PrintGen      uint64

	// Terminal and output funnel.
	Term inputTerminal
	Out  io.Writer

	// Coalesced repaint state.
	RefreshMu      sync.Mutex
	LastRefresh    time.Time
	RefreshPending bool
}

// ChainState is the fork-detection tracker: tip, heights, sync set,
// persisted tip, wire index and render cache. Mu serializes tip
// updates; take Mu only while holding DisplayMu, never take DisplayMu
// while holding Mu.
type ChainState struct {
	Mu               sync.Mutex
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
}

// VerifyState is the async badge-verify worker: channel, guard,
// close-once and the autoverify toggle.
type VerifyState struct {
	VerifyCh        chan verifyJob
	VerifyMu        sync.Mutex
	VerifyCloseOnce sync.Once
	AutoVerify      bool
	AutoVerifyMu    sync.RWMutex
}

// PendingState is the outgoing message state: sequence counters,
// one-shot targets, placeholders, early echoes and banner staging.
// Mu guards the placeholder/echo queues shared by the input loop
// and the pump; take Mu only while holding DisplayMu, never take
// DisplayMu while holding Mu.
type PendingState struct {
	Mu              sync.Mutex
	TmpSeq          uint64
	PendingReplyTo  uint64
	ReplyDraft      uint64
	LastMessageTime time.Time

	PendingPlaceholders []pendingMsg
	PendingEchoes       []pendingEcho

	PendingDateBanner     string
	PendingDateBannerWire *WireMessage
}

// NewSession returns a Session with the channels, maps and buffers that
// main() previously created alongside the first use.
func NewSession() *Session {
	return &Session{
		Quitting: make(chan bool, 1),
		PumpDone: make(chan struct{}),
		TripPrev: make([]byte, 32),
		Verify: VerifyState{
			VerifyCh: make(chan verifyJob, 128),
		},
		Pending: PendingState{
			PendingPlaceholders: []pendingMsg{},
		},
		Chain: ChainState{
			SyncHashes: map[string]bool{},
		},
	}
}

// connect dials, resolves secrets and authenticates. False means
// main must stop; a non-nil Conn is closed by the caller.
func (s *Session) connect() bool {
	s.WSURL = normalizeURL(CLI.Server)
	s.Username = strings.TrimSpace(CLI.Username)

	// Fail fast on version policy before prompting for secrets.
	if !checkServerVersion(s.WSURL) {
		os.Exit(1)
	}

	// Tripcode is a secret: -t takes no value. Resolve it here, before
	// dialing: the prompts (secret, save offer, unlock) are interactive
	// and would blow the server's 12s auth-response deadline if they ran
	// after the s.Challenge. Key derivation still happens after the
	// s.Challenge so serverPub stays salt-bound.
	if CLI.UseTripcode && CLI.Tripcode == "" {
		tc, terr := resolveTripcode(true, CLI.ConfigDir, s.Username, CLI.Server)
		if terr != nil {
			fmt.Printf("❌ Tripcode: %v\n", terr)
			notifyQuit()
			return false
		}
		CLI.Tripcode = tc
	}

	var err error
	dialConn, dialURL, err := dialWithUpgrade(s.WSURL)
	if err != nil {
		return false
	}
	s.Conn = dialConn
	s.WSURL = dialURL

	s.Challenge, err = readChallenge(s.Conn)
	if err != nil {
		return false
	}

	// Derive trip key after s.Challenge so serverPub is known for salt binding
	// s.Pending.TmpSeq numbers every outgoing message in this session (trip and
	// plain alike). The server relays it verbatim but never assigns it.
	// The base is random per connection (upper 32 bits) so a reconnect
	// never reuses another session's IDs in stash/pending matching.
	// CSPRNG: a predictable base would let an observer pre-compute
	// placeholder collisions.
	s.Pending.TmpSeq = (uint64(rand.Uint32()) + 1) << 32
	var seed [4]byte
	if _, rerr := cryptorand.Read(seed[:]); rerr == nil {
		s.Pending.TmpSeq = (uint64(binary.BigEndian.Uint32(seed[:])) + 1) << 32
	}
	passphraseBytes := []byte(CLI.Tripcode)
	if len(passphraseBytes) > 0 {
		priv, pub, badge := deriveTripKey(CLI.Tripcode, s.Challenge.ServerPubKey)
		s.TripPriv = priv
		s.TripPub = pub
		s.TripBadge = badge
		// Zero passphrase copy
		for i := range passphraseBytes {
			passphraseBytes[i] = 0
		}
		CLI.Tripcode = ""
	}

	respPacket := AuthPacket{
		Username: s.Username,
		Nonce:    s.Challenge.Nonce,
	}
	// Replay filtering follows the same knob as live display: -j asks
	// for join/leave lines in catch-up history too.
	respPacket.HistoryJoins = CLI.ShowJoin
	if s.TripPub != nil {
		respPacket.TripPub = hex.EncodeToString(s.TripPub)
		// Legacy Tripcode field not needed when TripPub is sent; keep empty
	} else {
		respPacket.Tripcode = CLI.Tripcode
	}

	if CLI.KeyFile != "" {
		idf, lerr := LoadIdentityFile(CLI.KeyFile)
		if lerr != nil {
			fmt.Printf("❌ %v\n", lerr)
			notifyQuit()
			return false
		}
		id := idf.Ed25519
		if id == nil {
			fmt.Println("❌ key.json không có danh tính ed25519.")
			notifyQuit()
			return false
		}
		respPacket.Role = id.Role
		privBytes, err := hex.DecodeString(id.PrivateKey)
		if err != nil || len(privBytes) != ed25519.PrivateKeySize {
			fmt.Println("❌ Private Key trong file không hợp lệ (Phải là chuỗi Hex 128 ký tự).")
			notifyQuit()
			return false
		}

		priv := ed25519.PrivateKey(privBytes)

		// Server pubkey pinning: verify server's identity before sending auth
		if s.Challenge.ServerPubKey != "" {
			if id.ServerPubKey != "" && !strings.EqualFold(id.ServerPubKey, s.Challenge.ServerPubKey) {
				fmt.Printf("🚨 Server identity mismatch! Pin %s != %s — abort.\n", strutil.ShortN(id.ServerPubKey, 12), strutil.ShortN(s.Challenge.ServerPubKey, 12))
				notifyQuit()
				return false
			}
			if s.Challenge.ServerSig != "" {
				srvPub, _ := hex.DecodeString(s.Challenge.ServerPubKey)
				srvSig, _ := hex.DecodeString(s.Challenge.ServerSig)
				msg := []byte("V2V-SERVER-v1\x00" + s.Challenge.Nonce + "\x00" + s.Challenge.ServerHost)
				if len(srvPub) == ed25519.PublicKeySize && len(srvSig) == ed25519.SignatureSize {
					if !ed25519.Verify(srvPub, msg, srvSig) {
						fmt.Println("❌ Server không chứng minh được private key — dừng.")
						notifyQuit()
						return false
					}
				}
			}
			if id.ServerPubKey == "" && s.Challenge.ServerPubKey != "" {
				fmt.Printf("⚠️ Lần đầu kết nối tới server %s pin %s…\n", s.Challenge.ServerHost, strutil.ShortN(s.Challenge.ServerPubKey, 16))
			}
		}
		// Use server's pubkey for anti-reuse (instead of host string)
		bindValue := ""
		if s.Challenge.ServerPubKey != "" {
			bindValue = s.Challenge.ServerPubKey
		} else if id.ServerPubKey != "" {
			bindValue = id.ServerPubKey
		} else {
			if u, perr := url.Parse(s.WSURL); perr == nil {
				bindValue = strings.ToLower(u.Hostname())
			}
		}
		dataToSign := s.Challenge.Nonce + "|" + id.Role + "|" + respPacket.Username + "|" + bindValue
		sig := ed25519.Sign(priv, []byte(dataToSign))
		respPacket.Signature = hex.EncodeToString(sig)

		h := hmac.New(sha512.New, []byte(id.HmacShield))
		h.Write(sig)
		h.Write([]byte(s.Challenge.Nonce))
		respPacket.Hmac = hex.EncodeToString(h.Sum(nil))

		fmt.Printf("🔑 Đang yêu cầu cấp quyền: [%s]...\n", id.Role)
	} else {
		// WebAuthn passkey login (web build only): failure already shown
		// via setWasmStatus; keep the runtime alive so late browser
		// callbacks (dialog dismissal, timers) don't hit a dead runtime.
		if !applyWebPasskey(&respPacket, s.Challenge.Nonce) {
			s.Conn.Close()
			parkForever()
			return false
		}
	}

	err = s.Conn.WriteJSON(respPacket)
	if err != nil {
		fmt.Println("❌ Lỗi gửi dữ liệu xác thực:", err)
		return false
	}

	var authSuccess AuthPacket
	err = s.Conn.ReadJSON(&authSuccess)
	if err != nil {
		fmt.Println("❌ Lỗi đọc phản hồi xác thực:", err)
		notifyQuit()
		return false
	}
	s.AuthType = orDefault(authSuccess.AuthType, "guest")
	s.Role = authSuccess.Role
	s.Unlimited = authSuccess.Perms != nil && authSuccess.Perms.CanMessageUnlimited
	s.Connected = time.Now()
	if authSuccess.Perms != nil {
		s.Prefix = authSuccess.Perms.CustomPrefix
	}
	// Sync trip chain state from server's last known seq/prev for this pub
	if s.TripPub != nil && authSuccess.TripPub != "" {
		s.TripSeq = authSuccess.TripSeq
		if authSuccess.TripPrev != "" {
			if b, err := hex.DecodeString(authSuccess.TripPrev); err == nil && len(b) == 32 {
				s.TripPrev = b
			}
		}
		if s.TripPrev == nil {
			s.TripPrev = make([]byte, 32)
		}
	}

	switch authSuccess.Type {
	case "auth_failed":
		msg := "❌ Xác thực bị từ chối: " + authSuccess.Error
		fmt.Println(msg)
		if showWasmStatus(msg, true) {
			s.Conn.Close()
			parkForever()
		}
		notifyQuit()
		return false
	case "auth_success":
		s.Username = authSuccess.Username
	}
	return true
}

// initUI opens the terminal, tabs, toggles and chain state.
func (s *Session) initUI() bool {
	var err error
	s.Display.Term, err = newInputTerminal()
	if err != nil {
		fmt.Println("❌ Lỗi khởi tạo terminal:", err)
		return false
	}
	s.Display.Out = s.Display.Term.Writer()

	s.Display.ShowJoinLeave = CLI.ShowJoin

	s.Verify.AutoVerify = true
	// s.Display.ShowMeta toggles the trailing "#height:hash" line (/meta, default
	// from ui.meta.show in config). The session command overrides
	// in-memory only; the chain still verifies when hidden.
	s.Display.ShowMeta = ClientCfg.ShowMeta()
	s.Pending.LastMessageTime = time.Now().Add(-10 * time.Second)

	s.Display.ActiveTab = TabChat
	cl, cb, sl, sb := tabCaps()
	s.Display.TabChat = newTabBuffer(cl, cb)
	s.Display.TabSys = newTabBuffer(sl, sb)

	s.initChainState()

	// s.Chain.WireIdx keeps full wires by height for /info lookups and rich
	// quotes. Populated on every render (s.Display.DisplayMu held by all callers).
	s.Chain.WireIdx = newWireIndex(1000)

	s.Chain.RenderCache = newRenderCache(200)
	return true
}

package main

import (
	"crypto/ed25519"
	"crypto/hmac"
	cryptorand "crypto/rand"
	"crypto/sha512"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math/rand/v2"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/CleveTok3125/V2V/lib/strutil"
)

// Session carries chat session state shared by the read pump, input loop,
// render path and chain tracker. Sub-state lives in the Display, Chain,
// Verify and Pending groups; the root keeps identity and connection
// lifecycle.
//
// Lock order is fixed Display -> Chain -> Pending, never the reverse.
// DisplayMu stays the god-lock for tab buffers and placeholder/echo
// state (they must stay atomic); ChainMu serializes tip updates between
// pump and input loop. Pump-side chain reads stay under caller-held
// DisplayMu with no ChainMu: the pump is their only writer.
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
	// BurnArmed records that /burn warned earlier in this session, so
	// /burn confirm cannot be the first command run.
	BurnArmed bool
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
	// quitOnce makes gracefulQuit idempotent: /quit, Ctrl+D and a read
	// failure can all reach it, and the socket teardown must run once.
	quitOnce sync.Once
	// SendMu serializes socket writes: the input loop owns them, and
	// the background PoW solver must not interleave frames.
	SendMu sync.Mutex
	// powBusy guards against overlapping in-chat PoW solves.
	powBusy atomic.Bool

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
	// Notify gates live printing of informational notices (/notify,
	// ui.notify.*). A muted notice still lands in TabSystem. Critical
	// warnings bypass the gate. Under NotifyMu.
	Notify    NotifyState
	NotifyMu  sync.RWMutex
	DisplayMu sync.Mutex
	ActiveTab int
	TabChat   *tabBuffer
	TabSys    *tabBuffer

	// LayoutEpoch counts the writes that move the cursor away from where the
	// last buffer entry ended. Only a tab switch does: it repaints the whole
	// screen, so the rows above the cursor stop belonging to the block that
	// was drawn last. The input prompt does not count — it is drawn below the
	// last entry, and every entry ends with a newline, so the cursor is still
	// one row past the block and moving up over it lands on its first row.
	LayoutEpoch uint64

	// Terminal and output funnel. When Sink is set the client runs the
	// Bubble Tea app: the session keeps its tab buffers and wakes the app,
	// which pulls the active tab and paints it.
	Term inputTerminal
	Sink uiSink

	// CatchupHold defers the shape of the join replay: while it is set,
	// recovered wires are spliced at their chain position instead of
	// appended and the greeting waits for the release, so a gapped replay
	// never reads as the final order. Under DisplayMu.
	CatchupHold bool
	// HoldGen invalidates the release timer of a superseded hold.
	HoldGen uint64
	// HoldTimer is the release timer for the current hold; stopped when
	// the hold ends so no stale timer lingers.
	HoldTimer *time.Timer
	// LastDateBanner is the most recent announced day (sys_date), so a
	// repeat — a connect announcement right after the replay already
	// carried one — is not printed twice. Empty disables the dedup.
	LastDateBanner string
	// PendingGreeting is the startup welcome line. It is held until the
	// catch-up history prints so it lands at the bottom of the loaded
	// history (or a short timer releases it when no replay arrives).
	// Under DisplayMu.
	PendingGreeting string

	// RevealProof remembers published seeds by lowercase pub hex, so /info
	// can show the public proof for an exposed identity. Populated from
	// reveal notices on every render path, muted or not. Under DisplayMu.
	RevealProof map[string]string
	// StatusMsg is a transient footer notice (history sync results).
	// Shown in the status bar until StatusUntil, then cleared. Under
	// DisplayMu like the other display state.
	StatusMsg   string
	StatusUntil time.Time

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
	Mu           sync.Mutex
	ChainTip     [32]byte
	ChainHeight  uint64
	ChainHaveTip bool
	ChainWarned  bool
	// ChainGapWarned latches the one-time slow-peer gap notice: a
	// forward height jump means frames were dropped in transit, which
	// must not consume the tamper latch (a later real break still
	// warns). Like the other chain fields, written under DisplayMu.
	ChainGapWarned   bool
	ServerPubHex     string
	TipPath          string
	PersistedTip     [32]byte
	PersistedHeight  uint64
	PersistedServer  string
	HavePersistedTip bool
	InSync           bool
	// InOlder marks an on-demand older segment in flight: its wires
	// predate the running tip, so the pump renders and indexes them
	// without link verification or echo matching. Set and cleared
	// under DisplayMu like InSync.
	InOlder bool
	// OlderExhausted records that the server answered the last older
	// request with an exhausted window (nothing below the oldest known
	// height), so page-up stops asking for more.
	OlderExhausted bool
	// OlderInsert/OlderInsertSys are where the next entry of the in-flight
	// older window is spliced into its buffer. Its wires predate everything
	// buffered, so they go before it, in arrival order. Reset on the window
	// header and advanced as entries land.
	OlderInsert    int
	OlderInsertSys int
	// OlderHeightBound and OlderSeqBound snapshot the oldest held line.
	//
	// Zero bounds splice everything for old servers.
	OlderHeightBound uint64
	OlderSeqBound    uint64
	// InRecover marks a recovery window in flight: the server is
	// refilling missed heights at or above the running tip's past.
	// Recovery wires verify against their own anchor (the tip at
	// detection time), never the running tip. Set on the recovery
	// header; cleared by any footer, the trailer, or a wire newer
	// than the pending window. Under DisplayMu like InSync.
	InRecover bool
	// RecoverRendered is set by checkRecoverWire when it renders a
	// recovered wire itself: the pump must then skip its own
	// renderChatBlock, otherwise recovered messages duplicate on
	// screen. Reset per wire by verifyReplayWire. Caller holds
	// DisplayMu.
	RecoverRendered bool
	// RecoverPending is the outstanding refill: the missed [From, To]
	// heights plus the anchor they must continue. Nil when idle.
	// Written under DisplayMu by the pump.
	RecoverPending *recoverWindow
	// RecoverGen invalidates stale refill timers (settle timeout and
	// retry scheduling) when a newer attempt supersedes them.
	RecoverGen uint64
	// LiveRecovered counts chained lines auto-refilled from live gaps
	// this session, against history.liveRecoverCap.
	LiveRecovered int
	// SyncHeights maps chained heights received during the initial load
	// to their hashes. Reset when the load starts; the fork check
	// compares the persisted tip's height against it. Under DisplayMu
	// like the other sync fields.
	SyncHeights map[uint64][32]byte
	// Loading tracks the client-driven initial load: pages are requested
	// ascending until LoadTarget lines are loaded, the announced window
	// end (LoadMaxSeq) is reached, or the server reports no more.
	// LoadStartSeq is the first seq expected, LoadLoaded the distinct
	// lines loaded so far, LoadRetried the current page's retry count.
	Loading      bool
	LoadStartSeq uint64
	LoadMaxSeq   uint64
	LoadTarget   int
	LoadLoaded   int
	LoadRetried  int
	// LoadHold holds live wires arriving during the initial load.
	//
	// Flushed after history, in arrival order.
	LoadHold     []heldLine
	TipSinceSave uint64
	WireIdx      *wireIndex
	RenderCache  *renderCache
	// Votes holds cumulative vote totals by chain height, folded from
	// system.vote lines. DisplayMu guards it like the tab buffers: every
	// pump render and vote apply runs under it.
	Votes map[uint64]VoteScore
}

// heldLine is a live line held during the initial load.
//
// JSON wire or raw legacy line, in arrival order.
type heldLine struct {
	msg   WireMessage
	raw   string
	isRaw bool
}

// VerifyState carries the autoverify toggle: whether a received badge is
// checked against the trip signature as it is rendered.
type VerifyState struct {
	AutoVerify bool
	// PowTier is the tier of the screen being solved, 0 when idle. It feeds
	// the status line so the footer shows the wait instead of a chat notice.
	PowTier      int
	AutoVerifyMu sync.RWMutex
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
	// SeenTmpIDs holds recently consumed message IDs so a late or
	// duplicate echo can be dropped instead of stashed (which would
	// age into a false "ID altered" warning). Bounded; oldest evicted.
	SeenTmpIDs []uint64

	// PendingDateBannerWire holds a date banner that arrived inside a
	// window whose chat lines are not drawn yet, so it prints just before
	// the block that follows it rather than in the wrong place.
	PendingDateBannerWire *WireMessage

	// EchoSlot is the block a just-consumed echo belongs to. The render that
	// follows writes into that slot instead of appending, so a message
	// replaces its own pending block in place rather than leaving it for a
	// separate erase. Cleared by whoever consumes it.
	EchoSlot *pendingMsg
}

// NewSession returns a Session with the channels, maps and buffers that
// main() previously created alongside the first use.
func NewSession() *Session {
	return &Session{
		Quitting: make(chan bool, 1),
		PumpDone: make(chan struct{}),
		TripPrev: make([]byte, 32),
		Pending: PendingState{
			PendingPlaceholders: []pendingMsg{},
		},
		Chain: ChainState{
			SyncHeights: map[uint64][32]byte{},
			Votes:       map[uint64]VoteScore{},
		},
		// No muted tags, so a Session that never runs initUI (tests,
		// embedded use) still prints every notice.
		Display: DisplayState{
			Notify: NotifyState{Muted: map[string]bool{}, PowMinTier: 1},
		},
	}
}

// connect dials, resolves secrets and authenticates. False means
// main must stop; a non-nil Conn is closed by the caller.
func (s *Session) connect() bool {
	// Before anything interactive: a login that names two credentials has
	// no valid interpretation.
	if err := validateAuthFlags(); err != nil {
		fmt.Println("❌ " + err.Error() + ".")
		notifyQuit()
		return false
	}
	s.WSURL = normalizeURL(CLI.Server)
	s.Username = strings.TrimSpace(CLI.Username)
	// The name is the cheapest thing to check and the one the server would
	// otherwise replace without a word, so it goes before the passkey wait
	// and the version probe.
	if err := validateDisplayName(s.Username); err != nil {
		msg := "❌ " + err.Error()
		fmt.Println(msg)
		if showWasmStatus(msg, true) {
			// The panel is back on screen with the reason; keep the runtime
			// alive so the next submit reuses it.
			parkForever()
		}
		notifyQuit()
		return false
	}

	// A passkey login needs a browser ceremony first, and that wait does
	// not belong on the chat socket: it takes minutes, and a socket held
	// that long costs a connection slot and can be cut by anything idle on
	// the path. The ticket comes back here, and the connection below
	// spends it.
	var pairTicket string
	if CLI.Passkey != "" {
		ticket, ok := acquirePairTicket(s.WSURL, CLI.Passkey)
		if !ok {
			notifyQuit()
			return false
		}
		pairTicket = ticket
	}

	// Fail fast on version policy before prompting for secrets.
	if !checkServerVersion(s.WSURL) {
		os.Exit(1)
	}

	// Tripcode is a secret: -t takes no value. Resolve it here, before
	// dialing: the prompts (secret, save offer, unlock) are interactive
	// and would blow the server's 12s auth-response deadline if they ran
	// after the s.Challenge. Key derivation still happens after the
	// s.Challenge so the server default salt is known.
	tripSalt := ""
	tripSaltSrc := "server default"
	if CLI.UseTripcode && CLI.Tripcode == "" {
		tc, salt, src, terr := resolveTripcode(true, CLI.ConfigDir, s.Username, CLI.Server)
		if terr != nil {
			fmt.Printf("❌ Tripcode: %v\n", terr)
			notifyQuit()
			return false
		}
		CLI.Tripcode = tc
		tripSalt = salt
		tripSaltSrc = src
	} else if CLI.Tripcode != "" {
		tripSalt, tripSaltSrc = customSaltWithSource()
	}

	var err error
	dialConn, dialURL, err := newDialer().DialManaged(s.WSURL)
	if err != nil {
		return false
	}
	s.Conn = dialConn
	s.WSURL = dialURL

	s.Challenge, err = s.readChallenge()
	if err != nil {
		return false
	}

	// Trip key defaults to the server salt; a custom salt makes the
	// identity portable across servers.
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
		fmt.Printf("🔑 Nguồn salt: %s.\n", formatSaltSource(tripSaltSrc, s.Challenge))
		salt := resolveTripSalt(tripSalt, tripServerDefault(s.Challenge))
		priv, pub, badge := deriveTripKey(CLI.Tripcode, salt)
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
		Platform: clientPlatform(),
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

	if CLI.Passkey != "" {
		respPacket.Role = CLI.Passkey
		respPacket.PairTicket = pairTicket
	} else if CLI.KeyFile != "" {
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
				fmt.Printf("🚨 Server identity mismatch! Pin %s != %s — abort.\n", serverField(strutil.ShortN(id.ServerPubKey, 12)), serverField(strutil.ShortN(s.Challenge.ServerPubKey, 12)))
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
				fmt.Printf("⚠️ Lần đầu kết nối tới server %s pin %s…\n", serverField(s.Challenge.ServerHost), serverField(strutil.ShortN(s.Challenge.ServerPubKey, 16)))
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

	authSuccess, err := readAuthResult(s)
	if err != nil {
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
		msg := "❌ Xác thực bị từ chối: " + serverField(authSuccess.Error)
		fmt.Println(msg)
		if showWasmStatus(msg, true) {
			s.Conn.Close()
			parkForever()
		}
		notifyQuit()
		return false
	case "auth_success":
		s.Username = authSuccess.Username
	default:
		// The server only ever answers with auth_success or auth_failed.
		// Anything else — a notice frame sent by mistake, a proxy, a
		// different service on the port — means we do not actually hold
		// a session, so refuse instead of opening a UI that will fail on
		// its first read.
		fmt.Println("❌ Phản hồi xác thực không hợp lệ: " + serverField(authSuccess.Type))
		if showWasmStatus("❌ Phản hồi xác thực không hợp lệ", true) {
			s.Conn.Close()
			parkForever()
		}
		notifyQuit()
		return false
	}
	return true
}

// initUI opens the terminal, tabs, toggles and chain state.
func (s *Session) initUI() bool {
	// The TUI app supplies the terminal: it owns the screen and hands the
	// session loop the lines the user submits.
	if s.Display.Term == nil {
		fmt.Println("❌ Lỗi: terminal chưa được khởi tạo")
		return false
	}

	s.Display.ShowJoinLeave = CLI.ShowJoin
	if !s.Display.ShowJoinLeave && ClientCfg != nil {
		s.Display.ShowJoinLeave = ClientCfg.Defaults.ShowJoin
	}

	s.Display.Notify = notifyStateFromConfig()
	s.applyQuietFlags()

	s.Verify.AutoVerify = ClientCfg.DefaultAutoVerify()
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
	if s.Chain.Votes == nil {
		s.Chain.Votes = make(map[uint64]VoteScore)
	}
	return true
}

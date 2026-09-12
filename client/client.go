package main

import (
	"crypto/ed25519"
	"crypto/hmac"
	cryptorand "crypto/rand"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/CleveTok3125/V2V/internal/codebg"
	"github.com/CleveTok3125/V2V/internal/filter"
	"github.com/CleveTok3125/V2V/internal/guard"
	"github.com/CleveTok3125/V2V/internal/linkify"
	"github.com/CleveTok3125/V2V/internal/markup"
	"github.com/CleveTok3125/V2V/internal/strutil"
	"github.com/CleveTok3125/V2V/internal/trip"
	"github.com/CleveTok3125/V2V/internal/tripcolor"
	"github.com/CleveTok3125/V2V/internal/wire"

	"github.com/alecthomas/kong"
)

var Version = "dev"

// renderChatText sanitizes incoming chat text and renders code spans with
// the configured highlight palette, falling back to compiled defaults
// when the client config is absent. Forum markup (bold, italic,
// strikethrough, links, quotes) renders through markup, which delegates
// code to codebg.

// Session-wide config, protocol aliases and shared terminal/socket
// surfaces. The render/parse helpers live in render.go; the session
// loop is main() below.

var CLI struct {
	Version     kong.VersionFlag `help:"Hiển thị phiên bản" short:"v"`
	Server      string           `help:"Link server WebSocket" short:"s"`
	Username    string           `help:"Tên người dùng của bạn" default:"Anonymous" short:"u"`
	UseTripcode bool             `help:"Dùng tripcode" short:"t"`
	Tripcode    string           `kong:"-"`
	UserAgent   string           `help:"Tùy chỉnh User-Agent" default:"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36" short:"a"`
	Info        bool             `help:"Kiểm tra thông tin trạng thái của Server" short:"i"`
	ShowJoin    bool             `help:"Hiện thông báo người dùng ra/vào phòng" short:"j"`

	UseKey    bool   `help:"Dùng key mặc định trong config-dir" short:"k"`
	KeyFile   string `help:"Đường dẫn file chứa khóa xác thực" short:"K" name:"key-file"`
	Proxy     string `help:"Proxy http/https/socks5"`
	AskProxy  bool   `help:"Hỏi thông tin proxy bằng prompt" name:"ask-proxy"`
	ConfigDir string `help:"Thư mục config" short:"c" env:"V2V_CONFIG_DIR"`
	CacheDir  string `help:"Thư mục cache/history" short:"C" env:"V2V_CACHE_DIR"`

	EncryptConfig bool `help:"Mã hóa config bằng passphrase" name:"encrypt-config"`
}

// Protocol schema lives in internal/wire (single source). Aliases keep
// every existing reference compiling while guaranteeing client and
// server serialize identically.
type (
	AuthPacket  = wire.AuthPacket
	WireMessage = wire.WireMessage
	TripMeta    = wire.TripMeta
	Permission  = wire.Permission
	HistorySync = wire.HistorySync
)

// WebSocket message type constants (RFC 6455) so the shared chat logic does
// not depend on a specific websocket implementation.
const (
	wsTextMessage  = 1
	wsCloseMessage = 8
)

// wsConn is the minimal socket surface the chat loop needs. The desktop build
// satisfies it with gorilla/websocket; the wasm build uses a shim over the
// browser's native WebSocket.
type wsConn interface {
	ReadJSON(v any) error
	WriteJSON(v any) error
	ReadMessage() (messageType int, p []byte, err error)
	WriteMessage(messageType int, data []byte) error
	Close() error
}

// inputTerminal abstracts the interactive input/output the chat loop uses.
// The desktop build wraps chzyer/readline (full line editing); the wasm build
// pipes bytes/lines to and from the browser terminal emulator.
type inputTerminal interface {
	ReadLine() (string, error)
	SetPrompt(p string)
	Refresh()
	Close()
	Writer() io.Writer
}

// dialWS opens the WebSocket connection (platform-specific, see ws_other.go /
// ws_wasm.go).
//
// newInputTerminal creates the interactive terminal (platform-specific, see
// input_other.go / input_wasm.go).
//
// parseFlags resolves CLI arguments / web config (platform-specific, see
// config_other.go / config_wasm.go).

// historyFile is set in parseFlags from CacheDir (UserCacheDir/V2V/history.tmp by default).
var historyFile string

type verifyJob struct {
	rawLine     string
	badge       string
	urlStr      string
	pub         string
	seqStr      string
	prev        string
	sig         string
	msgHash     string
	serverPub   string
	displayName string
	textParam   string
	seq         uint32
	tmpID       uint64
	tmpReplyTo  uint64
}

// emitWhoami prints the /whoami identity lines under the session DisplayMu. The mutex
// is released on every path via defer: guests (empty role) take no early
// return that could skip the unlock and freeze the terminal.
func emitWhoami(mu *sync.Mutex, emit func(string), uname, authType, role string, unlimited bool, prefix string) {
	mu.Lock()
	defer mu.Unlock()
	emit(fmt.Sprintf("| [Local]: Người dùng: %s | Xác thực: %s\n", uname, authType))
	if role != "" {
		emit(fmt.Sprintf("| [Local]: Role: %s | Unlimited: %v | Prefix: %q\n", role, unlimited, prefix))
	}
}

func main() {
	parseFlags()

	if CLI.EncryptConfig {
		encryptConfigFile()
		return
	}

	if CLI.Info {
		checkServerInfo(CLI.Server)
		return
	}

	if CLI.Server == "" {
		fmt.Println("❌ Lỗi: Vui lòng cung cấp link server bằng cờ -s (VD: -s ws://localhost:8080)")
		return
	}

	sess := NewSession()

	sess.WSURL = normalizeURL(CLI.Server)
	sess.Username = strings.TrimSpace(CLI.Username)

	// Fail fast on version policy before prompting for secrets.
	if !checkServerVersion(sess.WSURL) {
		os.Exit(1)
	}

	// Tripcode is a secret: -t takes no value. Resolve it here, before
	// dialing: the prompts (secret, save offer, unlock) are interactive
	// and would blow the server's 12s auth-response deadline if they ran
	// after the challenge. Key derivation still happens after the
	// challenge so serverPub stays salt-bound.
	if CLI.UseTripcode && CLI.Tripcode == "" {
		tc, terr := resolveTripcode(true, CLI.ConfigDir, sess.Username, CLI.Server)
		if terr != nil {
			fmt.Printf("❌ Tripcode: %v\n", terr)
			notifyQuit()
			return
		}
		CLI.Tripcode = tc
	}

	var err error
	dialConn, dialURL, err := dialWithUpgrade(sess.WSURL)
	if err != nil {
		return
	}
	sess.Conn = dialConn
	sess.WSURL = dialURL
	defer sess.Conn.Close()

	challenge, err := readChallenge(sess.Conn)
	if err != nil {
		return
	}

	// Derive trip key after challenge so serverPub is known for salt binding
	var tripPriv ed25519.PrivateKey
	var tripPub ed25519.PublicKey
	var tripBadge string
	var tripSeq uint32
	var tripPrev []byte = make([]byte, 32)
	// tmpSeq numbers every outgoing message in this session (trip and
	// plain alike). The server relays it verbatim but never assigns it.
	// The base is random per connection (upper 32 bits) so a reconnect
	// never reuses another session's IDs in stash/pending matching.
	// CSPRNG: a predictable base would let an observer pre-compute
	// placeholder collisions.
	var tmpSeq uint64 = (uint64(rand.Uint32()) + 1) << 32
	var seed [4]byte
	if _, rerr := cryptorand.Read(seed[:]); rerr == nil {
		tmpSeq = (uint64(binary.BigEndian.Uint32(seed[:])) + 1) << 32
	}
	// pendingReplyTo quotes a chain height on the next outgoing message
	// only (/reply sets it, the send path consumes and clears it).
	var pendingReplyTo uint64
	// replyDraft holds a quote target awaiting its body on the next line
	// (bare "/reply H" form). Any slash command or empty-line ^C aborts it.
	var replyDraft uint64
	passphraseBytes := []byte(CLI.Tripcode)
	if len(passphraseBytes) > 0 {
		priv, pub, badge := deriveTripKey(CLI.Tripcode, challenge.ServerPubKey)
		tripPriv = priv
		tripPub = pub
		tripBadge = badge
		// Zero passphrase copy
		for i := range passphraseBytes {
			passphraseBytes[i] = 0
		}
		CLI.Tripcode = ""
	}

	respPacket := AuthPacket{
		Username: sess.Username,
		Nonce:    challenge.Nonce,
	}
	// Replay filtering follows the same knob as live display: -j asks
	// for join/leave lines in catch-up history too.
	respPacket.HistoryJoins = CLI.ShowJoin
	if tripPub != nil {
		respPacket.TripPub = hex.EncodeToString(tripPub)
		// Legacy Tripcode field not needed when TripPub is sent; keep empty
	} else {
		respPacket.Tripcode = CLI.Tripcode
	}

	if CLI.KeyFile != "" {
		idf, lerr := LoadIdentityFile(CLI.KeyFile)
		if lerr != nil {
			fmt.Printf("❌ %v\n", lerr)
			notifyQuit()
			return
		}
		id := idf.Ed25519
		if id == nil {
			fmt.Println("❌ key.json không có danh tính ed25519.")
			notifyQuit()
			return
		}
		respPacket.Role = id.Role
		privBytes, err := hex.DecodeString(id.PrivateKey)
		if err != nil || len(privBytes) != ed25519.PrivateKeySize {
			fmt.Println("❌ Private Key trong file không hợp lệ (Phải là chuỗi Hex 128 ký tự).")
			notifyQuit()
			return
		}

		priv := ed25519.PrivateKey(privBytes)

		// Server pubkey pinning: verify server's identity before sending auth
		if challenge.ServerPubKey != "" {
			if id.ServerPubKey != "" && !strings.EqualFold(id.ServerPubKey, challenge.ServerPubKey) {
				fmt.Printf("🚨 Server identity mismatch! Pin %s != %s — abort.\n", strutil.ShortN(id.ServerPubKey, 12), strutil.ShortN(challenge.ServerPubKey, 12))
				notifyQuit()
				return
			}
			if challenge.ServerSig != "" {
				srvPub, _ := hex.DecodeString(challenge.ServerPubKey)
				srvSig, _ := hex.DecodeString(challenge.ServerSig)
				msg := []byte("V2V-SERVER-v1\x00" + challenge.Nonce + "\x00" + challenge.ServerHost)
				if len(srvPub) == ed25519.PublicKeySize && len(srvSig) == ed25519.SignatureSize {
					if !ed25519.Verify(srvPub, msg, srvSig) {
						fmt.Println("❌ Server không chứng minh được private key — dừng.")
						notifyQuit()
						return
					}
				}
			}
			if id.ServerPubKey == "" && challenge.ServerPubKey != "" {
				fmt.Printf("⚠️ Lần đầu kết nối tới server %s pin %s…\n", challenge.ServerHost, strutil.ShortN(challenge.ServerPubKey, 16))
			}
		}
		// Use server's pubkey for anti-reuse (instead of host string)
		bindValue := ""
		if challenge.ServerPubKey != "" {
			bindValue = challenge.ServerPubKey
		} else if id.ServerPubKey != "" {
			bindValue = id.ServerPubKey
		} else {
			if u, perr := url.Parse(sess.WSURL); perr == nil {
				bindValue = strings.ToLower(u.Hostname())
			}
		}
		dataToSign := challenge.Nonce + "|" + id.Role + "|" + respPacket.Username + "|" + bindValue
		sig := ed25519.Sign(priv, []byte(dataToSign))
		respPacket.Signature = hex.EncodeToString(sig)

		h := hmac.New(sha512.New, []byte(id.HmacShield))
		h.Write(sig)
		h.Write([]byte(challenge.Nonce))
		respPacket.Hmac = hex.EncodeToString(h.Sum(nil))

		fmt.Printf("🔑 Đang yêu cầu cấp quyền: [%s]...\n", id.Role)
	} else {
		// WebAuthn passkey login (web build only): failure already shown
		// via setWasmStatus; keep the runtime alive so late browser
		// callbacks (dialog dismissal, timers) don't hit a dead runtime.
		if !applyWebPasskey(&respPacket, challenge.Nonce) {
			sess.Conn.Close()
			parkForever()
			return
		}
	}

	err = sess.Conn.WriteJSON(respPacket)
	if err != nil {
		fmt.Println("❌ Lỗi gửi dữ liệu xác thực:", err)
		return
	}

	var authSuccess AuthPacket
	err = sess.Conn.ReadJSON(&authSuccess)
	if err != nil {
		fmt.Println("❌ Lỗi đọc phản hồi xác thực:", err)
		notifyQuit()
		return
	}
	var (
		sessAuthType  = orDefault(authSuccess.AuthType, "guest")
		sessRole      = authSuccess.Role
		sessUnlimited = authSuccess.Perms != nil && authSuccess.Perms.CanMessageUnlimited
		sessPrefix    string
		sessConnected = time.Now()
	)
	if authSuccess.Perms != nil {
		sessPrefix = authSuccess.Perms.CustomPrefix
	}
	// Sync trip chain state from server's last known seq/prev for this pub
	if tripPub != nil && authSuccess.TripPub != "" {
		tripSeq = authSuccess.TripSeq
		if authSuccess.TripPrev != "" {
			if b, err := hex.DecodeString(authSuccess.TripPrev); err == nil && len(b) == 32 {
				tripPrev = b
			}
		}
		if tripPrev == nil {
			tripPrev = make([]byte, 32)
		}
	}

	switch authSuccess.Type {
	case "auth_failed":
		msg := "❌ Xác thực bị từ chối: " + authSuccess.Error
		fmt.Println(msg)
		if showWasmStatus(msg, true) {
			sess.Conn.Close()
			parkForever()
		}
		notifyQuit()
		return
	case "auth_success":
		sess.Username = authSuccess.Username
	}

	sess.Term, err = newInputTerminal()
	if err != nil {
		fmt.Println("❌ Lỗi khởi tạo terminal:", err)
		return
	}
	defer sess.Term.Close()
	defer ClearLoadedPassphrase()
	sess.Out = sess.Term.Writer()

	quitting := make(chan bool, 1)
	showJoinLeave := CLI.ShowJoin
	var showJoinMu sync.RWMutex

	verifyCh := make(chan verifyJob, 128)
	var verifyCloseOnce sync.Once
	autoVerify := true
	var autoVerifyMu sync.RWMutex
	// showMeta toggles the trailing "#height:hash" line (/meta, default
	// from ui.meta.show in config). The session command overrides
	// in-memory only; the chain still verifies when hidden.
	showMeta := ClientCfg.ShowMeta()
	var showMetaMu sync.RWMutex
	serverPubForVerify := challenge.ServerPubKey
	lastMessageTime := time.Now().Add(-10 * time.Second)

	sess.ActiveTab = TabChat
	cl, cb, sl, sb := tabCaps()
	sess.TabChat = newTabBuffer(cl, cb)
	sess.TabSys = newTabBuffer(sl, sb)

	sess.initChainState()

	// sess.WireIdx keeps full wires by height for /info lookups and rich
	// quotes. Populated on every render (sess.DisplayMu held by all callers).
	sess.WireIdx = newWireIndex(1000)

	sess.RenderCache = newRenderCache(200)
	go func() {
		for job := range verifyCh {
			// Use shared trip verification (same as server) — serverPub is enforced to server's own key
			serverPub := strings.ToLower(job.serverPub)
			if serverPub == "" {
				serverPub = strings.ToLower(serverPubForVerify)
			}
			textForVerify := job.textParam
			// Links without text= verify the signature over msgHash alone;
			// nothing is displayed from the link, so no text binding exists.
			_, err := trip.Verify(trip.VerifyParams{
				Text:          textForVerify,
				DisplayName:   job.displayName,
				ServerPub:     serverPub,
				PubHex:        job.pub,
				Seq:           job.seq,
				PrevHex:       job.prev,
				SigHex:        job.sig,
				MsgHashHex:    job.msgHash,
				TmpID:         job.tmpID,
				ReplyTo:       job.tmpReplyTo,
				SkipTextCheck: textForVerify == "",
			})
			valid := err == nil
			// Fallback: if textParam was empty but msgHash check failed, try empty text path
			if !valid && textForVerify != "" {
				// Already handled; keep invalid
			}
			var colored string
			if valid {
				colored = badgeColor(job.badge) + job.badge + "\x1b[0m"
			} else {
				colored = "\x1b[91m" + job.badge + " ✗\x1b[0m"
			}
			line := fmt.Sprintf("  └─ ✍️ \x1b]8;;%s\x1b\\%s\x1b]8;;\x1b\\", job.urlStr, colored)
			sess.DisplayMu.Lock()
			sess.emitTab(TabChat, fmt.Sprintf("| %s\n", filter.SanitizeForDisplay(line)))
			sess.DisplayMu.Unlock()
			sess.refreshCoalesced()
		}
	}()

	go func() {
		var pendingDateBanner string
		var pendingDateBannerWire *WireMessage
		// flushDateBannerLocked prints a stashed date banner to TabSystem
		// before the block that follows it. Caller must hold sess.DisplayMu.
		flushDateBannerLocked := func() {
			if pendingDateBanner != "" {
				sess.emitTab(TabSystem, fmt.Sprintf("| %s\n", filter.SanitizeForDisplay(pendingDateBanner)))
				pendingDateBanner = ""
			}
			if pendingDateBannerWire != nil {
				sess.renderChatBlock(*pendingDateBannerWire)
				pendingDateBannerWire = nil
			}
		}
		// handleHistorySync consumes a replay trailer from either a whole
		// frame or a coalesced per-line blob: never rendered, only the
		// fork check runs. Caller refreshes after.
		handleHistorySync := func(hs HistorySync) {
			sess.DisplayMu.Lock()
			sess.InSync = false
			if warn, flush := forkWarning(hs, sess.HavePersistedTip, sess.PersistedTip, sess.PersistedHeight, sess.SyncHashes); warn != "" {
				sess.emitLocalFeedback(warn)
				if flush {
					sess.flushChainTip()
				}
			}
			sess.DisplayMu.Unlock()
		}
		for {
			_, msg, err := sess.Conn.ReadMessage()
			if err != nil {
				select {
				case <-quitting:
					return
				default:
					fmt.Fprintf(sess.Out, "\r\033[K\n ❌ Mất kết nối server\n")
					// Flush before exit: os.Exit skips deferred
					// sess.Term.Close/flushChainTip, losing the newest tip and
					// leaving the terminal raw.
					sess.flushChainTip()
					sess.Term.Close()
					ClearLoadedPassphrase()
					os.Exit(1)
				}
			}

			showJoinMu.RLock()
			isShowingJoin := showJoinLeave
			showJoinMu.RUnlock()

			// Try to handle structured WireMessage JSON first (for new protocol)
			var wire WireMessage
			if err := json.Unmarshal(msg, &wire); err == nil && wire.Type == "chat" {
				sess.DisplayMu.Lock()
				sess.consumeEchoLocked(wire, true)
				sess.checkChainLink(wire)
				flushDateBannerLocked()
				sess.renderChatBlock(wire)
				sess.DisplayMu.Unlock()
				sess.refreshCoalesced()
				continue
			}
			var sysWire WireMessage
			if err := json.Unmarshal(msg, &sysWire); err == nil && sysWire.Type == "system" {
				sess.DisplayMu.Lock()
				sess.checkChainLink(sysWire)
				if !isShowingJoin && isDateBanner(sysWire) {
					pendingDateBannerWire = &sysWire
					sess.DisplayMu.Unlock()
					sess.refreshCoalesced()
					continue
				}
				if !isShowingJoin && isJoinLeave(sysWire) {
					sess.DisplayMu.Unlock()
					sess.refreshCoalesced()
					continue
				}
				flushDateBannerLocked()
				sess.renderChatBlock(sysWire)
				sess.DisplayMu.Unlock()
				sess.refreshCoalesced()
				continue
			}
			// Machine-readable replay trailer: never rendered, only the
			// fork check below consumes it.
			if hs, ok := parseHistorySync(msg); ok {
				handleHistorySync(hs)
				sess.refreshCoalesced()
				continue
			}
			for _, line := range strings.Split(string(msg), "\n") {
				// Also try per-line JSON (for history blob where each line is a WireMessage JSON)
				var wl WireMessage
				if hs, ok := parseHistorySync([]byte(line)); ok {
					handleHistorySync(hs)
					continue
				}
				if err := json.Unmarshal([]byte(line), &wl); err == nil && (wl.Type == "chat" || wl.Type == "system") {
					sess.DisplayMu.Lock()
					if wl.Type == "chat" {
						sess.consumeEchoLocked(wl, !sess.InSync)
					}
					sess.checkChainLink(wl)
					if wl.Type == "system" && !isShowingJoin && isDateBanner(wl) {
						pendingDateBannerWire = &wl
						sess.DisplayMu.Unlock()
						continue
					}
					if wl.Type == "system" && !isShowingJoin && isJoinLeave(wl) {
						sess.DisplayMu.Unlock()
						continue
					}
					flushDateBannerLocked()
					sess.renderChatBlock(wl)
					sess.DisplayMu.Unlock()
					continue
				}
				if !isShowingJoin && isDateBannerLine(line) {
					pendingDateBanner = line
					continue
				}
				if !isShowingJoin && isJoinLeaveSystemLine(line) {
					continue
				}
				if boundary, start := parseHistoryBoundary(line); boundary {
					sess.DisplayMu.Lock()
					if start {
						sess.InSync = true
					} else {
						pendingDateBanner = ""
						pendingDateBannerWire = nil
						sess.InSync = false
					}
					sess.emitTab(TabChat, fmt.Sprintf("| %s\n", filter.SanitizeForDisplay(line)))
					sess.DisplayMu.Unlock()
					continue
				}
				if !isShowingJoin && (pendingDateBanner != "" || pendingDateBannerWire != nil) {
					sess.DisplayMu.Lock()
					flushDateBannerLocked()
					sess.DisplayMu.Unlock()
				}
				if isTripBadgeLine(line) {
					autoVerifyMu.RLock()
					av := autoVerify
					autoVerifyMu.RUnlock()
					if av {
						if job, ok := parseTripBadgeLine(line); ok {
							// Drop-oldest on full: dropped is treated as verify fail (deterministic)
							// Show the line immediately as pending-plain then queue newest for real verify
							// If queue was full, oldest was dropped and will stay uncolored (fail)
							sess.enqueueVerify(job)
							continue
						}
					}
				}
				sess.DisplayMu.Lock()
				sess.emitTab(classifyTab(line), fmt.Sprintf("| %s\n", filter.SanitizeForDisplay(line)))
				sess.DisplayMu.Unlock()
			}
			sess.refreshCoalesced()
		}
	}()

	greeting(sess.Out, sess.Username)

	// gracefulQuit closes the connection cleanly like /quit does, so both
	// an explicit quit command and an EOF (Ctrl+D) leave no dangling state.
	gracefulQuit := func() {
		quitting <- true
		sess.flushChainTip()
		verifyCloseOnce.Do(func() { close(verifyCh) })
		sess.Conn.WriteMessage(wsCloseMessage, []byte{})
		// Zero trip private key
		if tripPriv != nil {
			for i := range tripPriv {
				tripPriv[i] = 0
			}
		}
		fmt.Fprintf(sess.Out, "👋 Đang ngắt kết nối... Tạm biệt!\n")
		time.Sleep(500 * time.Millisecond)
		notifyQuit()
	}

	for {
		text, err := sess.Term.ReadLine()
		if err != nil {
			if errors.Is(err, ErrInputCancel) {
				// Reply targets attach to the next send only; reset first so a
				// rejected message never leaks its quote into a later one. The
				// draft/inline /reply handlers below re-arm it when due.
				pendingReplyTo = 0
				if replyDraft > 0 {
					replyDraft = 0
					sess.Term.SetPrompt("| > ")
					sess.DisplayMu.Lock()
					sess.emitLocalFeedback("| [Local]: Đã hủy reply nháp.\n")
					sess.DisplayMu.Unlock()
					sess.Term.Refresh()
					continue
				}
				sess.Term.SetPrompt("| > ")
				sess.DisplayMu.Lock()
				sess.emitLocalFeedback("| [Local]: Ctrl+C chỉ hủy dòng nhập, thoát app bằng Ctrl+D.\n")
				sess.DisplayMu.Unlock()
				sess.Term.Refresh()
				continue
			}
			gracefulQuit()
			break
		}

		text = strings.TrimSpace(text)
		if text == "" {
			continue
		}
		if replyDraft > 0 {
			if strings.HasPrefix(text, "/") {
				// Any slash command aborts the draft, then runs normally
				// through the dispatch below (a "/" body could never send
				// anyway: the unknown-slash guard rejects it).
				replyDraft = 0
				sess.Term.SetPrompt("| > ")
			} else {
				// Draft body (codeblock fences included): attach and send.
				pendingReplyTo = replyDraft
				replyDraft = 0
				sess.Term.SetPrompt("| > ")
			}
		}
		if text == "/quit" || text == "/q" {
			gracefulQuit()
			break
		}

		if text == "/whoami" || text == "/w" {
			emitWhoami(&sess.DisplayMu, sess.emitLocalFeedback, sess.Username, sessAuthType, sessRole, sessUnlimited, sessPrefix)
			continue
		}

		if text == "/status" {
			showJoinMu.RLock()
			sj := "TẮT"
			if showJoinLeave {
				sj = "BẬT"
			}
			showJoinMu.RUnlock()
			sess.DisplayMu.Lock()
			sess.emitLocalFeedback(fmt.Sprintf("| [Local]: Server: %s | Đã kết nối: %s | Phiên bản: %s | Show-join: %s\n",
				sess.WSURL, time.Since(sessConnected).Round(time.Second), Version, sj))
			sess.DisplayMu.Unlock()
			continue
		}

		if text == "/help" || text == "/h" {
			sess.DisplayMu.Lock()
			sess.emitLocalFeedback("  [Trợ giúp]: Danh sách các lệnh có thể sử dụng:\n")
			sess.emitLocalFeedback("    - /help, /h      : Hiển thị bảng trợ giúp này\n")
			sess.emitLocalFeedback("    - /clear, /c     : Xóa sạch màn hình chat\n")
			sess.emitLocalFeedback("    - /clearhistory, /ch: Xóa file lịch sử gõ phím lưu trên máy\n")
			sess.emitLocalFeedback("    - /quit, /q      : Rời phòng chat và tắt ứng dụng\n")
			sess.emitLocalFeedback("    - /showjoin, /sj : Bật/tắt hiện thông báo người khác ra vào phòng cho các tin kế tiếp\n")
			sess.emitLocalFeedback("    - /whoami, /w    : Thông tin danh tính và quyền hiện tại\n")
			sess.emitLocalFeedback("    - /status        : Trạng thái kết nối và phiên bản client\n")
			sess.emitLocalFeedback("    - /autoverify, /av: Bật/tắt auto-verify trip (mặc định BẬT, queue FIFO, verify song song)\n")
			sess.emitLocalFeedback("    - /info <n>[:hash]: Xem đầy đủ metadata tin nhắn (verify lại tại local)\n")
			sess.emitLocalFeedback("    - /expand <n>, /xpan  : Mở đầy đủ tin bị thu gọn (vd /expand 1234)\n")
			sess.emitLocalFeedback("    - /copy <n>[:hash]: Copy nội dung thô tin nhắn vào clipboard\n")
			sess.emitLocalFeedback("    - /tab, /t [1|2]  : Chuyển tab chat / local & system\n")
			sess.emitLocalFeedback("    - /meta, /m [on|off]: Hiện/ẩn dòng meta #height:hash (mặc định hiện, chain vẫn verify)\n")
			sess.emitLocalFeedback("    - /find, /f <n>[:hash]: Tìm tin theo số height trong bộ nhớ (vd /find 1234)\n")
			sess.emitLocalFeedback("    - /reply <n>[:hash] text: Trả lời tin #n kèm quote (vd /reply 1234 đồng ý)\n")
			sess.emitLocalFeedback("    - /reply <n>          : Soạn reply nháp, dòng tiếp theo là nội dung\n")
			sess.emitLocalFeedback("    - Gõ @#n (vd @#1234) trong tin để nhắc tới tin khác (sáng lên khi còn trong bộ nhớ)\n")
			sess.emitLocalFeedback("    - Lệnh lạ bắt đầu bằng / bị chặn, không gửi đi (muốn gửi chữ / đầu dòng thì dùng codeblock)\n")
			sess.emitLocalFeedback("    - Gõ ``` ở đầu và cuối tin nhắn để gửi Code block / nhiều dòng (^C hủy nhập)\n")
			sess.emitLocalFeedback("    - Bọc chữ trong `dấu backtick` để hiện nền riêng (inline code một dòng)\n")
			sess.DisplayMu.Unlock()
			continue
		}

		if text == "/showjoin" || text == "/sj" {
			showJoinMu.Lock()
			showJoinLeave = !showJoinLeave
			status := "ĐÃ TẮT"
			if showJoinLeave {
				status = "ĐÃ BẬT"
			}
			showJoinMu.Unlock()
			sess.DisplayMu.Lock()
			sess.emitLocalFeedback(fmt.Sprintf("| [Local]: %s hiển thị thông báo người dùng ra/vào phòng cho các tin kế tiếp.\n", status))
			sess.DisplayMu.Unlock()
			continue
		}

		if text == "/autoverify" || text == "/av" {
			autoVerifyMu.Lock()
			autoVerify = !autoVerify
			status := "BẬT"
			if !autoVerify {
				status = "TẮT"
			}
			autoVerifyMu.Unlock()
			sess.DisplayMu.Lock()
			sess.emitLocalFeedback(fmt.Sprintf("| [Local]: Auto-verify đã %s (mặc định BẬT, verify song song qua channel FIFO).\n", status))
			sess.DisplayMu.Unlock()
			continue
		}

		if text == "/tab" || text == "/t" || strings.HasPrefix(text, "/tab ") || strings.HasPrefix(text, "/t ") {
			n := sess.ActiveTab
			if text == "/tab" || text == "/t" {
				if sess.ActiveTab == TabChat {
					n = TabSystem
				} else {
					n = TabChat
				}
			} else {
				rest := ""
				if strings.HasPrefix(text, "/tab ") {
					rest = strings.TrimSpace(strings.TrimPrefix(text, "/tab"))
				} else {
					rest = strings.TrimSpace(strings.TrimPrefix(text, "/t"))
				}
				if rest == "1" {
					n = TabChat
				} else if rest == "2" {
					n = TabSystem
				}
			}
			sess.switchTab(n)
			sess.DisplayMu.Lock()
			sess.emitLocalFeedback(tabBarLine(sess.ActiveTab))
			sess.PrintGen++
			sess.DisplayMu.Unlock()
			continue
		}

		if text == "/clear" || text == "/c" {
			fmt.Fprint(sess.Out, "\033[H\033[2J")
			greeting(sess.Out, sess.Username)
			continue
		}

		if text == "/reply" || strings.HasPrefix(text, "/reply ") {
			if !ClientCfg.ReplyEnabled() {
				sess.DisplayMu.Lock()
				sess.emitLocalFeedback("| [Local]: Reply đã tắt trong config (ui.reply.enabled).\n")
				sess.DisplayMu.Unlock()
				sess.Term.Refresh()
				continue
			}
			rest := strings.TrimSpace(strings.TrimPrefix(text, "/reply"))
			fields := strings.Fields(rest)
			var target, body string
			if len(fields) > 0 {
				target = fields[0]
				body = strings.TrimSpace(rest[len(target):])
			}
			height, suffix, err := parseFindArg(target)
			if err != nil {
				sess.DisplayMu.Lock()
				sess.emitLocalFeedback("| [Local]: Dùng /reply <height>[:hash] [tin nhắn] (vd /reply 1234 đồng ý; /reply 1234 để soạn nháp).\n")
				sess.DisplayMu.Unlock()
				sess.Term.Refresh()
				continue
			}
			sess.DisplayMu.Lock()
			found := len(findMetaMatches(sess.TabChat.lines, height, suffix)) > 0 ||
				len(findMetaMatches(sess.TabSys.lines, height, suffix)) > 0
			sess.DisplayMu.Unlock()
			if !found {
				sess.DisplayMu.Lock()
				sess.emitLocalFeedback(fmt.Sprintf("| [Local]: Tin #%d không còn trong bộ nhớ, không reply được.\n", height))
				sess.DisplayMu.Unlock()
				sess.Term.Refresh()
				continue
			}
			if body == "" {
				// Draft mode: quote now, body on the next line. Any slash
				// command or empty-line ^C aborts it (see loop top).
				replyDraft = height
				sess.Term.SetPrompt(fmt.Sprintf("| ↩ #%d > ", height))
				sess.DisplayMu.Lock()
				for _, q := range sess.quoteLinesFor(height, false) {
					fmt.Fprint(sess.Out, q+"\n")
					sess.PrintGen++
				}
				sess.emitLocalFeedback("| [Local]: Gõ nội dung reply (Enter gửi, ^C ở dòng trống hủy, lệnh / khác hủy draft).\n")
				sess.DisplayMu.Unlock()
				sess.Term.Refresh()
				continue
			}
			// Quote validated: the body flows through the normal dispatch
			// below (codeblock collection, guards, send) with the target
			// attached one-shot.
			pendingReplyTo = height
			text = body
		}

		if text == "/meta" || text == "/m" || strings.HasPrefix(text, "/meta ") || strings.HasPrefix(text, "/m ") {
			rest := ""
			if strings.HasPrefix(text, "/meta") {
				rest = strings.TrimSpace(strings.TrimPrefix(text, "/meta"))
			} else {
				rest = strings.TrimSpace(strings.TrimPrefix(text, "/m"))
			}
			showMetaMu.Lock()
			switch rest {
			case "on":
				showMeta = true
			case "off":
				showMeta = false
			case "":
				showMeta = !showMeta
			default:
				showMetaMu.Unlock()
				sess.DisplayMu.Lock()
				sess.emitLocalFeedback("| [Local]: Dùng /meta, /meta on hoặc /meta off.\n")
				sess.DisplayMu.Unlock()
				sess.Term.Refresh()
				continue
			}
			state := "HIỆN"
			if !showMeta {
				state = "ẨN"
			}
			showMetaMu.Unlock()
			sess.DisplayMu.Lock()
			sess.emitLocalFeedback(fmt.Sprintf("| [Local]: Dòng meta (#height:hash) %s (chain vẫn verify ngầm).\n", state))
			sess.DisplayMu.Unlock()
			sess.Term.Refresh()
			continue
		}

		if text == "/find" || text == "/f" || strings.HasPrefix(text, "/find ") || strings.HasPrefix(text, "/f ") {
			rest := ""
			if strings.HasPrefix(text, "/find") {
				rest = strings.TrimSpace(strings.TrimPrefix(text, "/find"))
			} else {
				rest = strings.TrimSpace(strings.TrimPrefix(text, "/f"))
			}
			height, suffix, err := parseFindArg(rest)
			if err != nil {
				sess.DisplayMu.Lock()
				sess.emitLocalFeedback(fmt.Sprintf("| [Local]: %v.\n", err))
				sess.DisplayMu.Unlock()
				sess.Term.Refresh()
				continue
			}
			sess.DisplayMu.Lock()
			shown := 0
			header := fmt.Sprintf("| [Local]: Tìm #%d", height)
			if suffix != "" {
				header += ":" + suffix
			}
			sess.emitLocalFeedback(header + " trong bộ nhớ:\n")
			// Snapshot matches before emitting: emitting appends to
			// sess.TabSys, whose eviction could shift indices mid-scan.
			var hits []string
			for _, buf := range []*tabBuffer{sess.TabChat, sess.TabSys} {
				matches := findMetaMatches(buf.lines, height, suffix)
				for _, idx := range matches {
					if idx > 0 {
						hits = append(hits, buf.lines[idx-1])
					}
					hits = append(hits, buf.lines[idx])
				}
				shown += len(matches)
			}
			for _, h := range hits {
				sess.emitLocalFeedback(h)
			}
			if shown == 0 {
				sess.emitLocalFeedback("| [Local]: Không thấy (tin cũ đã bị evict khỏi bộ nhớ hoặc chưa sync).\n")
			}
			sess.DisplayMu.Unlock()
			sess.Term.Refresh()
			continue
		}

		if text == "/expand" || text == "/xpan" || strings.HasPrefix(text, "/expand ") || strings.HasPrefix(text, "/xpan ") {
			rest := strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(text, "/expand"), "/xpan"))
			height, _, err := parseFindArg(rest)
			if err != nil {
				sess.DisplayMu.Lock()
				sess.emitLocalFeedback("| [Local]: Dùng /expand #height (vd /expand 1234).\n")
				sess.DisplayMu.Unlock()
				sess.Term.Refresh()
				continue
			}
			sess.DisplayMu.Lock()
			wire, ok := sess.WireIdx.get(height)
			if !ok {
				sess.emitLocalFeedback("| [Local]: Tin đã trôi khỏi bộ nhớ hoặc chưa sync.\n")
				sess.DisplayMu.Unlock()
				sess.Term.Refresh()
				continue
			}
			if findCollapsed(sess.TabChat.lines, height) < 0 {
				sess.emitLocalFeedback("| [Local]: Tin này không thu gọn.\n")
				sess.DisplayMu.Unlock()
				sess.Term.Refresh()
				continue
			}
			// Re-render full and replay it inside a dim heredoc frame
			// (like shell <<EOF): the delimiters mark history replay
			// without touching the verbatim content. No buffer surgery.
			autoVerifyMu.RLock()
			av := autoVerify
			autoVerifyMu.RUnlock()
			showMetaMu.RLock()
			withMeta := showMeta
			showMetaMu.RUnlock()
			_, full, _, _, _ := sess.buildChatBlock(wire, av, withMeta)
			sess.emitLocalFeedback(fmt.Sprintf("| [Local]: \x1b[90m<<<<<<< #%d\x1b[0m\n", height))
			for _, line := range strings.Split(strings.TrimSuffix(full, "\n"), "\n") {
				sess.emitLocalFeedback(line + "\n")
			}
			sess.emitLocalFeedback(fmt.Sprintf("| [Local]: \x1b[90m>>>>>>> #%d\x1b[0m\n", height))
			sess.DisplayMu.Unlock()
			sess.Term.Refresh()
			continue
		}

		if text == "/info" || strings.HasPrefix(text, "/info ") {
			rest := strings.TrimSpace(strings.TrimPrefix(text, "/info"))
			height, suffix, err := parseFindArg(rest)
			if err != nil {
				sess.DisplayMu.Lock()
				sess.emitLocalFeedback("| [Local]: Dùng /info <height>[:hash] (vd /info 1234).\n")
				sess.DisplayMu.Unlock()
				sess.Term.Refresh()
				continue
			}
			sess.DisplayMu.Lock()
			wire, ok := sess.WireIdx.get(height)
			if !ok {
				sess.emitLocalFeedback(fmt.Sprintf("| [Local]: Tin #%d không còn trong bộ nhớ (legacy không có metadata, hoặc đã evict).\n", height))
				sess.DisplayMu.Unlock()
				sess.Term.Refresh()
				continue
			}
			if suffix != "" && !strings.HasPrefix(strings.ToLower(wire.ChainHash), suffix) {
				sess.emitLocalFeedback("| [Local]: Height đúng nhưng hash khác — kiểm tra lại số.\n")
				sess.DisplayMu.Unlock()
				sess.Term.Refresh()
				continue
			}
			for _, line := range formatInfoBlock(wire) {
				sess.emitLocalFeedback(line)
			}
			sess.DisplayMu.Unlock()
			sess.Term.Refresh()
			continue
		}

		if text == "/copy" || strings.HasPrefix(text, "/copy ") {
			rest := strings.TrimSpace(strings.TrimPrefix(text, "/copy"))
			height, _, err := parseFindArg(rest)
			if err != nil {
				sess.DisplayMu.Lock()
				sess.emitLocalFeedback("| [Local]: Dùng /copy <height>[:hash] (vd /copy 1234).\n")
				sess.DisplayMu.Unlock()
				sess.Term.Refresh()
				continue
			}
			sess.DisplayMu.Lock()
			wire, ok := sess.WireIdx.get(height)
			if !ok {
				sess.emitLocalFeedback(fmt.Sprintf("| [Local]: Tin #%d không còn trong bộ nhớ (legacy không có metadata, hoặc đã evict).\n", height))
				sess.DisplayMu.Unlock()
				sess.Term.Refresh()
				continue
			}
			if err := copyToClipboard(wire.Text); err != nil {
				sess.emitLocalFeedback(fmt.Sprintf("| [Local]: %v.\n", err))
			} else {
				sess.emitLocalFeedback(fmt.Sprintf("| [Local]: Đã copy nội dung tin #%d.\n", height))
				scheduleClipboardClear(wire.Text, ClientCfg.ClipboardClearAfterSec())
			}
			sess.DisplayMu.Unlock()
			sess.Term.Refresh()
			continue
		}

		if text == "/clearhistory" || text == "/ch" {
			os.Remove(historyFile)
			sess.DisplayMu.Lock()
			sess.emitLocalFeedback(fmt.Sprintf("🗑️ Đã xóa file lịch sử gõ phím tại: %s\n", historyFile))
			sess.DisplayMu.Unlock()
			continue
		}

		// Unknown slash input: every built-in command was already matched
		// above, so anything still starting with "/" is a mistyped command
		// rejected locally and never broadcast. Known commands above
		// already continued.
		if isUnknownSlashCommand(text) {
			sess.DisplayMu.Lock()
			sess.emitLocalFeedback(fmt.Sprintf("| [Local]: Lệnh không tồn tại: %s. Gõ /help để xem danh sách.\n", text))
			sess.DisplayMu.Unlock()
			sess.Term.Refresh()
			continue
		}

		typedLinesCount := 1

		if strings.HasPrefix(text, "```") {
			if !codebg.NeedsContinuation(text) {
				// Single-line fence (```code```): complete already.
				typedLinesCount = 1
			} else {
				var canceled bool
				text, canceled = collectCodeblock(sess.Term, text)
				if canceled {
					continue
				}
				typedLinesCount = strings.Count(text, "\n") + 1
			}
		}

		if err := filter.ValidateMessage(text); err != nil {
			sess.DisplayMu.Lock()
			sess.emitLocalFeedback(fmt.Sprintf("| [Local]: Tin nhắn chứa ký tự không hợp lệ và đã bị chặn (client-side): %v\n", err))
			sess.DisplayMu.Unlock()
			sess.Term.Refresh()
			continue
		}

		// Guard: client-side MessageCooldown (mirror server, zero-trust)
		if ClientCfg != nil {
			if err := guard.ValidateMessageForSend(text, lastMessageTime, &guard.Limits{
				MaxMessageLength: ClientCfg.Limits.MaxMessageLength,
				MaxMessageLine:   ClientCfg.Limits.MaxMessageLine,
				MessageCooldown:  ClientCfg.Limits.MessageCooldown,
			}, false); err != nil {
				if err == guard.ErrTooFast {
					sess.DisplayMu.Lock()
					sess.emitLocalFeedback(fmt.Sprintf("| [Local]: Bạn đang chat quá nhanh! Vui lòng đợi %v.\n", ClientCfg.Limits.MessageCooldown))
					sess.DisplayMu.Unlock()
					sess.Term.Refresh()
					continue
				}
				if err == guard.ErrTooLong {
					sess.DisplayMu.Lock()
					sess.emitLocalFeedback(fmt.Sprintf("| [Local]: Tin nhắn quá dài (tối đa %d ký tự).\n", ClientCfg.Limits.MaxMessageLength))
					sess.DisplayMu.Unlock()
					sess.Term.Refresh()
					continue
				}
			}
		}

		// Placeholder: keep original text grey with pending indicator until server echo
		// Single sess.DisplayMu lock for entire wipe + placeholder + trip sign + send to avoid burst drift
		phRows := 0
		phShown := sess.ActiveTab == TabChat
		phBufEnd := 0
		sess.DisplayMu.Lock()
		for range typedLinesCount {
			fmt.Fprint(sess.Out, "\033[1A\033[2K\r")
		}

		// Render markup on the whole text first so fenced blocks
		// keep their state across lines; phRows then counts rendered
		// rows (headers added, closers dropped), matching the erase math.
		// Plain rendering (no highlight): highlight's full resets would
		// cancel the grey placeholder wrapper mid-line, and line counts
		// match the highlighted echo anyway.
		lines := strings.Split(markup.SpanPlain(text), "\n")
		phRows = len(lines)
		// Quoted target previews first (same helper as the echo path, so
		// both blocks share the shape; pending quotes carry ⏳ so the
		// erase region check keeps passing).
		if pendingReplyTo > 0 {
			for _, q := range sess.quoteLinesFor(pendingReplyTo, true) {
				sess.emitTab(TabChat, q+"\n")
				phRows++
			}
		}
		for i, line := range lines {
			line = linkify.Linkify(line)
			if i == 0 {
				sess.emitTab(TabChat, fmt.Sprintf("\x1b[90m| Bạn: %s ⏳\x1b[0m\n", line))
			} else {
				sess.emitTab(TabChat, fmt.Sprintf("\x1b[90m|      %s\x1b[0m\n", line))
			}
		}
		// Trip placeholder (grey ◆ …) — real badge will come from server echo
		if tripPriv != nil || CLI.Tripcode != "" {
			badgePlaceholder := tripBadge
			if badgePlaceholder == "" && CLI.Tripcode != "" {
				h := sha256.Sum256([]byte(CLI.Tripcode))
				badgePlaceholder = hex.EncodeToString(h[:])[:8]
			}
			if badgePlaceholder != "" {
				sess.emitTab(TabChat, fmt.Sprintf("\x1b[90m|  └─ ✍ ◆ %s ⏳\x1b[0m\n", badgePlaceholder))
				phRows++
			}
		}
		// Trailing meta line: every block ends with exactly one meta row so
		// the echo (carrying the real #height:hash) replaces it in place.
		// The chain position is unknown until the server echo arrives.
		// Hidden with /meta off; the echo follows the same session flag,
		// so row counts stay consistent.
		showMetaMu.RLock()
		pmMeta := showMeta
		showMetaMu.RUnlock()
		if pmMeta {
			sess.emitTab(TabChat, "\x1b[90m|   └─  ··· ⏳\x1b[0m\n")
			phRows++
		}
		phBufEnd = len(sess.TabChat.lines)
		sess.Term.Refresh()
		sess.DisplayMu.Unlock()

		tmpSeq++
		if tripPriv != nil {
			// Sign message with trip chain — bind displayName for anti-spoof
			tripSeq++
			msgHash := sha256.Sum256([]byte(text))
			prevCopy := make([]byte, len(tripPrev))
			copy(prevCopy, tripPrev)
			payload := tripcolor.CanonicalPayload(strings.ToLower(challenge.ServerPubKey), tripSeq, prevCopy, msgHash[:], []byte(tripPub), sess.Username, tmpSeq, pendingReplyTo)
			sig := ed25519.Sign(tripPriv, payload)
			h := sha256.New()
			h.Write(prevCopy)
			h.Write(sig)
			h.Write(msgHash[:])
			newPrev := h.Sum(nil)
			copy(tripPrev, newPrev)
			tripMsg := TripMessage{Text: text, Pub: hex.EncodeToString([]byte(tripPub)), Seq: tripSeq, Prev: hex.EncodeToString(prevCopy), Sig: hex.EncodeToString(sig), DisplayName: sess.Username, TmpID: tmpSeq, ReplyTo: pendingReplyTo}
			err = sess.Conn.WriteJSON(tripMsg)
			if err != nil {
				// Rollback seq/prev on send failure to avoid permanent fork
				tripSeq--
				copy(tripPrev, prevCopy)
				tmpSeq--
			}
		} else {
			// Unsigned chat always travels in an envelope carrying the
			// session counter; raw text is rejected by the server.
			err = sess.Conn.WriteJSON(PlainMessage{TmpID: tmpSeq, Text: text, ReplyTo: pendingReplyTo})
			if err != nil {
				tmpSeq--
			}
		}
		// Reply targets are one-shot: consumed by the send above whether
		// it succeeded or not (a failed send ends the session anyway).
		// Reset happens after placeholder tracking below, which records
		// the target for echo matching.
		if err != nil {
			// Mark placeholder as failed (red) is handled by server unicast; keep placeholder grey until then
			lastMessageTime = time.Now()
		} else {
			lastMessageTime = time.Now()
			// Track placeholder so the server echo can replace it.
			sess.DisplayMu.Lock()
			pm := pendingMsg{text: text, rows: phRows, shown: phShown, gen: sess.PrintGen, bufEnd: phBufEnd, sentAt: time.Now(), tmpID: tmpSeq, replyTo: pendingReplyTo}
			if tripPriv != nil {
				pm.hasTrip = true
				pm.seq = tripSeq
				pm.pub = hex.EncodeToString([]byte(tripPub))
			}
			sess.PendingPlaceholders = append(sess.PendingPlaceholders, pm)
			// Bound the queue: echoes that never arrive (dead server, old
			// build) must not grow memory or turn matching quadratic.
			// Evicted entries stay grey on screen: honestly unconfirmed.
			// Linear scan stays trivial at this bound, so no index map.
			const maxPendingPlaceholders = 128
			for len(sess.PendingPlaceholders) > maxPendingPlaceholders {
				sess.PendingPlaceholders = sess.PendingPlaceholders[1:]
			}
			// The echo may have beaten us here (local echo race): if a
			// stashed echo matches, erase the placeholder at once. The
			// echo itself was already rendered when it arrived.
			var haveStashed bool
			sess.PendingEchoes, _, haveStashed = takeStashedEcho(sess.PendingEchoes, pm.tmpID)
			if haveStashed {
				for i, p := range sess.PendingPlaceholders {
					if p.tmpID == pm.tmpID {
						sess.PendingPlaceholders = append(sess.PendingPlaceholders[:i], sess.PendingPlaceholders[i+1:]...)
						break
					}
				}
				sess.erasePlaceholderLocked(pm)
			}
			sess.DisplayMu.Unlock()
		}
		// Reply targets are one-shot, cleared after tracking above (the
		// pending entry already captured the target for echo matching).
		pendingReplyTo = 0
		if err != nil {
			fmt.Println("❌ Lỗi gửi tin nhắn:", err)
			break
		}

	}
}

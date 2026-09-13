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
	// after the sess.Challenge. Key derivation still happens after the
	// sess.Challenge so serverPub stays salt-bound.
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

	sess.Challenge, err = readChallenge(sess.Conn)
	if err != nil {
		return
	}

	// Derive trip key after sess.Challenge so serverPub is known for salt binding
	// sess.TmpSeq numbers every outgoing message in this session (trip and
	// plain alike). The server relays it verbatim but never assigns it.
	// The base is random per connection (upper 32 bits) so a reconnect
	// never reuses another session's IDs in stash/pending matching.
	// CSPRNG: a predictable base would let an observer pre-compute
	// placeholder collisions.
	sess.TmpSeq = (uint64(rand.Uint32()) + 1) << 32
	var seed [4]byte
	if _, rerr := cryptorand.Read(seed[:]); rerr == nil {
		sess.TmpSeq = (uint64(binary.BigEndian.Uint32(seed[:])) + 1) << 32
	}
	passphraseBytes := []byte(CLI.Tripcode)
	if len(passphraseBytes) > 0 {
		priv, pub, badge := deriveTripKey(CLI.Tripcode, sess.Challenge.ServerPubKey)
		sess.TripPriv = priv
		sess.TripPub = pub
		sess.TripBadge = badge
		// Zero passphrase copy
		for i := range passphraseBytes {
			passphraseBytes[i] = 0
		}
		CLI.Tripcode = ""
	}

	respPacket := AuthPacket{
		Username: sess.Username,
		Nonce:    sess.Challenge.Nonce,
	}
	// Replay filtering follows the same knob as live display: -j asks
	// for join/leave lines in catch-up history too.
	respPacket.HistoryJoins = CLI.ShowJoin
	if sess.TripPub != nil {
		respPacket.TripPub = hex.EncodeToString(sess.TripPub)
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
		if sess.Challenge.ServerPubKey != "" {
			if id.ServerPubKey != "" && !strings.EqualFold(id.ServerPubKey, sess.Challenge.ServerPubKey) {
				fmt.Printf("🚨 Server identity mismatch! Pin %s != %s — abort.\n", strutil.ShortN(id.ServerPubKey, 12), strutil.ShortN(sess.Challenge.ServerPubKey, 12))
				notifyQuit()
				return
			}
			if sess.Challenge.ServerSig != "" {
				srvPub, _ := hex.DecodeString(sess.Challenge.ServerPubKey)
				srvSig, _ := hex.DecodeString(sess.Challenge.ServerSig)
				msg := []byte("V2V-SERVER-v1\x00" + sess.Challenge.Nonce + "\x00" + sess.Challenge.ServerHost)
				if len(srvPub) == ed25519.PublicKeySize && len(srvSig) == ed25519.SignatureSize {
					if !ed25519.Verify(srvPub, msg, srvSig) {
						fmt.Println("❌ Server không chứng minh được private key — dừng.")
						notifyQuit()
						return
					}
				}
			}
			if id.ServerPubKey == "" && sess.Challenge.ServerPubKey != "" {
				fmt.Printf("⚠️ Lần đầu kết nối tới server %s pin %s…\n", sess.Challenge.ServerHost, strutil.ShortN(sess.Challenge.ServerPubKey, 16))
			}
		}
		// Use server's pubkey for anti-reuse (instead of host string)
		bindValue := ""
		if sess.Challenge.ServerPubKey != "" {
			bindValue = sess.Challenge.ServerPubKey
		} else if id.ServerPubKey != "" {
			bindValue = id.ServerPubKey
		} else {
			if u, perr := url.Parse(sess.WSURL); perr == nil {
				bindValue = strings.ToLower(u.Hostname())
			}
		}
		dataToSign := sess.Challenge.Nonce + "|" + id.Role + "|" + respPacket.Username + "|" + bindValue
		sig := ed25519.Sign(priv, []byte(dataToSign))
		respPacket.Signature = hex.EncodeToString(sig)

		h := hmac.New(sha512.New, []byte(id.HmacShield))
		h.Write(sig)
		h.Write([]byte(sess.Challenge.Nonce))
		respPacket.Hmac = hex.EncodeToString(h.Sum(nil))

		fmt.Printf("🔑 Đang yêu cầu cấp quyền: [%s]...\n", id.Role)
	} else {
		// WebAuthn passkey login (web build only): failure already shown
		// via setWasmStatus; keep the runtime alive so late browser
		// callbacks (dialog dismissal, timers) don't hit a dead runtime.
		if !applyWebPasskey(&respPacket, sess.Challenge.Nonce) {
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
	sess.AuthType = orDefault(authSuccess.AuthType, "guest")
	sess.Role = authSuccess.Role
	sess.Unlimited = authSuccess.Perms != nil && authSuccess.Perms.CanMessageUnlimited
	sess.Connected = time.Now()
	if authSuccess.Perms != nil {
		sess.Prefix = authSuccess.Perms.CustomPrefix
	}
	// Sync trip chain state from server's last known seq/prev for this pub
	if sess.TripPub != nil && authSuccess.TripPub != "" {
		sess.TripSeq = authSuccess.TripSeq
		if authSuccess.TripPrev != "" {
			if b, err := hex.DecodeString(authSuccess.TripPrev); err == nil && len(b) == 32 {
				sess.TripPrev = b
			}
		}
		if sess.TripPrev == nil {
			sess.TripPrev = make([]byte, 32)
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

	sess.ShowJoinLeave = CLI.ShowJoin

	sess.AutoVerify = true
	// sess.ShowMeta toggles the trailing "#height:hash" line (/meta, default
	// from ui.meta.show in config). The session command overrides
	// in-memory only; the chain still verifies when hidden.
	sess.ShowMeta = ClientCfg.ShowMeta()
	sess.LastMessageTime = time.Now().Add(-10 * time.Second)

	sess.ActiveTab = TabChat
	cl, cb, sl, sb := tabCaps()
	sess.TabChat = newTabBuffer(cl, cb)
	sess.TabSys = newTabBuffer(sl, sb)

	sess.initChainState()

	// sess.WireIdx keeps full wires by height for /info lookups and rich
	// quotes. Populated on every render (sess.DisplayMu held by all callers).
	sess.WireIdx = newWireIndex(1000)

	sess.RenderCache = newRenderCache(200)
	go sess.runVerify()

	go sess.runPump()

	greeting(sess.Out, sess.Username)

	for {
		text, err := sess.Term.ReadLine()
		if err != nil {
			if sess.handleReadErr(err) == cmdQuit {
				break
			}
			continue
		}

		text = strings.TrimSpace(text)
		if text == "" {
			continue
		}
		sess.handleDraftGate(text)
		act := sess.dispatch(text)
		if act == cmdQuit {
			break
		}
		if act == cmdDone {
			continue
		}

		body, lines, ok := sess.collectBody(text)
		if !ok {
			continue
		}
		if !sess.checkSendGuards(body) {
			continue
		}
		phRows, phShown, phBufEnd := sess.renderPlaceholder(body, lines)
		if serr := sess.sendMessage(body, phRows, phShown, phBufEnd); serr != nil {
			break
		}

	}
}

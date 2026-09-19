package main

import (
	"fmt"
	"io"
	"strings"
	"sync"

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
	HistoryRequest = wire.HistoryRequest
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
	if !sess.connect() {
		if sess.Conn != nil {
			sess.Conn.Close()
		}
		return
	}
	defer sess.Conn.Close()
	if !sess.initUI() {
		return
	}
	defer sess.Display.Term.Close()
	defer ClearLoadedPassphrase()
	go sess.runVerify()

	go sess.runPump()

	greeting(sess.Display.Out, sess.Username)

	for {
		text, err := sess.Display.Term.ReadLine()
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
		text, act := sess.dispatch(text)
		if act == cmdQuit {
			break
		}
		if act == cmdDone {
			continue
		}

		body, lines, ok := sess.collectBody(text)
		if !ok {
			// Codeblock canceled: no send happens, so drop the
			// one-shot reply target (inline /reply or draft).
			sess.Pending.PendingReplyTo = 0
			continue
		}
		if !sess.checkSendGuards(body) {
			// Guard rejected the message: same one-shot cleanup;
			// only sendMessage may consume PendingReplyTo.
			sess.Pending.PendingReplyTo = 0
			continue
		}
		phRows, phShown, phBufEnd := sess.renderPlaceholder(body, lines)
		if serr := sess.sendMessage(body, phRows, phShown, phBufEnd); serr != nil {
			break
		}

	}
}

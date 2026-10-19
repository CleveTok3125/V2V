package main

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"

	"github.com/CleveTok3125/V2V/lib/clientcore"
	"github.com/CleveTok3125/V2V/lib/wire"

	"github.com/alecthomas/kong"
)

var Version = "dev"

// renderChatText sanitizes incoming chat text and renders code spans with
// the configured highlight palette, falling back to compiled defaults
// when the client config is absent. Forum markup (bold, italic,
// strikethrough, links, quotes) renders through markup, which delegates
// code to codebg. Bare http(s) URLs are wrapped as OSC8 hyperlinks so
// they are clickable on the web build too.

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
	Quiet       []string         `help:"Ẩn thông báo theo tag (lặp được, ví dụ system.pow), hoặc all" name:"quiet" short:"Q"`

	UseKey    bool   `help:"Dùng key mặc định trong config-dir" short:"k"`
	KeyFile   string `help:"Đường dẫn file chứa khóa xác thực" short:"K" name:"key-file"`
	Passkey   string `help:"Đăng nhập bằng passkey của role này qua link trình duyệt" short:"P" name:"passkey"`
	Proxy     string `help:"Proxy http/https/socks5"`
	AskProxy  bool   `help:"Hỏi thông tin proxy bằng prompt" name:"ask-proxy"`
	ConfigDir string `help:"Thư mục config" short:"c" env:"V2V_CONFIG_DIR"`
	CacheDir  string `help:"Thư mục cache/history" short:"C" env:"V2V_CACHE_DIR"`

	EncryptConfig bool `help:"Mã hóa config bằng passphrase" name:"encrypt-config"`
}

// Protocol schema lives in lib/wire (single source). Aliases keep
// every existing reference compiling while guaranteeing client and
// server serialize identically.
type (
	AuthPacket     = wire.AuthPacket
	WireMessage    = wire.WireMessage
	TripMeta       = wire.TripMeta
	Permission     = wire.Permission
	HistorySync    = wire.HistorySync
	HistoryRequest = wire.HistoryRequest
	HeightRange    = wire.HeightRange
	HistoryInfo    = wire.HistoryInfo
	PowOffer       = wire.PowOffer
	PowResult      = wire.PowResult
	PowDecline     = wire.PowDecline
	VoteRequest    = wire.VoteRequest
)

// WebSocket message type constants (RFC 6455) so the shared chat logic does
// not depend on a specific websocket implementation.
const (
	wsTextMessage  = 1
	wsCloseMessage = 8
)

// wsConn is the core's socket surface. Aliased so the client's dialers,
// session and tests keep naming it the way they always have.
type wsConn = clientcore.Conn

// clientReadLimit bounds one inbound WebSocket frame. The server sends
// replay lines individually, so any legitimate frame is far below this;
// the budget is the configured history byte cap plus a small margin so a
// malicious server cannot force an unbounded allocation.
func clientReadLimit() int64 {
	const margin = 1 << 20
	if ClientCfg == nil {
		return margin
	}
	if v := int64(ClientCfg.Limits.MaxHistoryBytes); v > 0 {
		return v + margin
	}
	return margin
}

// inputTerminal abstracts the interactive input/output the chat loop uses.
// The real terminal is the Bubble Tea app (see tui.go and tui_other.go /
// tui_wasm.go); tests use a scripted fake.
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
// runTUI runs the Bubble Tea app (platform-specific, see tui_other.go /
// tui_wasm.go).
//
// parseFlags resolves CLI arguments / web config (platform-specific, see
// config_other.go / config_wasm.go).

// historyFile is set in parseFlags from CacheDir (UserCacheDir/V2V/history.tmp by default).
var historyFile string

// emitWhoami prints the /whoami identity block under the session DisplayMu as
// one entry: every entry carries a trailing gap row, so a line per entry would
// space the block out. The mutex is released on every path via defer: guests
// (empty role) take no early return that could skip the unlock and freeze the
// terminal.
func emitWhoami(mu *sync.Mutex, emit func(string), uname, authType, role string, unlimited bool, prefix string) {
	mu.Lock()
	defer mu.Unlock()
	rows := []string{fmt.Sprintf("[Local]: Người dùng: %s | Xác thực: %s", serverField(uname), serverField(authType))}
	if role != "" {
		rows = append(rows, fmt.Sprintf("  Role: %s | Unlimited: %v | Prefix: %q", serverField(role), unlimited, serverField(prefix)))
	}
	emit(strings.Join(rows, "\n"))
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

	// The app owns the screen and the input box; the session keeps the
	// state machine and the tab buffers. Wire it before initUI so every
	// buffer change reaches the app.
	tui := newApp(sess)
	sess.Display.Term = tui.term
	sess.Display.Sink = tui.sink

	if !sess.initUI() {
		return
	}
	defer sess.Display.Term.Close()
	defer ClearLoadedPassphrase()

	// The welcome line is held, not shown yet: it is released after the
	// join replay so it lands at the bottom of the loaded history
	// instead of above it. A short timer in the pump releases it when
	// no replay arrives.
	sess.Display.PendingGreeting = "\nĐã kết nối với username: " + serverField(sess.Username) + "\nGõ tin nhắn để chat, /help để hiện trợ giúp\n\n"

	go sess.runPump()
	go sess.runInputLoop()

	if err := runTUI(tui); err != nil {
		fmt.Fprintln(os.Stderr, "❌ Lỗi TUI:", err)
	}
	sess.gracefulQuit()
	notifyQuit()
}

// runInputLoop is the session's input half: it reads submitted lines from
// the terminal (the TUI app on a real run) and drives dispatch and send.
// The app owns the draft, so a submitted line already carries every
// newline the user typed.
func (s *Session) runInputLoop() {
	for {
		text, err := s.Display.Term.ReadLine()
		if err != nil {
			if s.handleReadErr(err) == cmdQuit {
				return
			}
			continue
		}

		text = strings.TrimSpace(text)
		if text == "" {
			continue
		}
		// The submitted line becomes a buffer entry the session owns: the
		// pending block and then the server echo replace it in place, so the
		// message never occupies two rows.
		submitted := text
		slot := s.echoInputLine(text)
		s.handleDraftGate(text)
		text, act := s.dispatch(text)
		// A known command belongs in the recall list; a message only when
		// the config opts in, and only once it is really sent. A rejected
		// slash line is not recorded: it may be message text.
		s.recordHistory(submitted, act == cmdDone || act == cmdQuit)
		if act == cmdQuit {
			return
		}
		if act == cmdDone || act == cmdReject {
			continue
		}

		if !s.checkSendGuards(text) {
			// Guard rejected the message: same one-shot cleanup; only
			// sendMessage may consume PendingReplyTo. The echo entry stays,
			// so the rejected line is still on the record.
			s.Pending.PendingReplyTo = 0
			continue
		}
		s.recordHistory(submitted, false)
		placeholder, slot, phShown := s.renderPlaceholder(text, slot)
		if serr := s.sendMessage(text, placeholder, slot, phShown); serr != nil {
			return
		}
	}
}

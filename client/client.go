package main

import (
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/CleveTok3125/V2V/codebg"
	"github.com/CleveTok3125/V2V/internal/chain"
	"github.com/CleveTok3125/V2V/internal/filter"
	"github.com/CleveTok3125/V2V/internal/guard"
	"github.com/CleveTok3125/V2V/internal/trip"
	"github.com/CleveTok3125/V2V/linkify"

	"github.com/alecthomas/kong"
)

var Version = "dev"

// renderChatText sanitizes incoming chat text and renders code spans with
// the configured highlight palette, falling back to compiled defaults
// when the client config is absent.
func renderChatText(text string) string {
	st := codebg.DefaultStyle()
	if ClientCfg != nil {
		cs := ClientCfg.UI.CodeStyle
		st = codebg.Style{
			Background: cs.Background,
			Keyword:    cs.Keyword,
			String:     cs.String,
			Comment:    cs.Comment,
			Number:     cs.Number,
			Name:       cs.Name,
			Function:   cs.Function,
			Type:       cs.Type,
			Operator:   cs.Operator,
		}
	}
	return codebg.RenderWithStyle(filter.SanitizeForDisplay(text), st)
}

var CLI struct {
	Version   kong.VersionFlag `help:"Hiển thị phiên bản (Git Commit Hash)" short:"v"`
	Server    string           `help:"Link server WebSocket" short:"s"`
	Username  string           `help:"Tên người dùng của bạn" default:"Anonymous" short:"u"`
	Tripcode  string           `help:"Mật khẩu bí mật để tạo Chữ ký Tripcode (tùy chọn)" short:"t"`
	UserAgent string           `help:"Tùy chỉnh User-Agent" default:"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36" short:"a"`
	Info      bool             `help:"Kiểm tra thông tin trạng thái của Server" short:"i"`
	ShowJoin  bool             `help:"Hiện thông báo người dùng ra/vào phòng" short:"j"`

	UseKey    bool   `help:"Dùng key mặc định trong config-dir" short:"k"`
	KeyFile   string `help:"Đường dẫn file chứa khóa xác thực" short:"K" name:"key-file"`
	ConfigDir string `help:"Thư mục config" short:"c" env:"V2V_CONFIG_DIR"`
	CacheDir  string `help:"Thư mục cache/history" short:"C" env:"V2V_CACHE_DIR"`
}

type AuthPacket struct {
	Type      string `json:"type"`
	Nonce     string `json:"nonce,omitempty"`
	Role      string `json:"role,omitempty"`
	Signature string `json:"signature,omitempty"`
	Hmac      string `json:"hmac,omitempty"`
	Username  string `json:"username,omitempty"`
	Tripcode  string `json:"tripcode,omitempty"`

	TripPub string `json:"trip_pub,omitempty"`

	PasskeyID         string `json:"passkey_id,omitempty"`
	PasskeyAuthData   string `json:"passkey_auth_data,omitempty"`
	PasskeyClientData string `json:"passkey_client_data,omitempty"`
	PasskeySig        string `json:"passkey_sig,omitempty"`

	ServerPubKey string `json:"server_pubkey,omitempty"`
	ServerSig    string `json:"server_sig,omitempty"`
	ServerHost   string `json:"server_host,omitempty"`

	Error    string      `json:"error,omitempty"`
	AuthType string      `json:"auth_type,omitempty"`
	Perms    *Permission `json:"perms,omitempty"`

	TripSeq  uint32 `json:"trip_seq,omitempty"`
	TripPrev string `json:"trip_prev,omitempty"`
}

type WireMessage struct {
	Type        string    `json:"type"`
	Time        string    `json:"time,omitempty"`
	DisplayName string    `json:"displayName,omitempty"`
	Text        string    `json:"text,omitempty"`
	Trip        *TripMeta `json:"trip,omitempty"`
	// TmpID is the sender's per-session counter, relayed verbatim.
	// ChainPrev/ChainHash/ChainHeight link the message into the global
	// hash chain (see server/chain.go); absent on legacy lines.
	TmpID       uint64 `json:"tmp_id,omitempty"`
	ChainPrev   string `json:"chain_prev,omitempty"`
	ChainHash   string `json:"chain_hash,omitempty"`
	ChainHeight uint64 `json:"chain_height,omitempty"`
}

type TripMeta struct {
	Pub       string `json:"pub"`
	Seq       uint32 `json:"seq"`
	Prev      string `json:"prev"`
	Sig       string `json:"sig"`
	ServerPub string `json:"server_pub"`
	MsgHash   string `json:"msg_hash,omitempty"`
	TmpID     uint64 `json:"tmp_id,omitempty"`
}

type Permission struct {
	CanMessageUnlimited bool   `json:"can_message_unlimited"`
	CustomPrefix        string `json:"custom_prefix"`
}

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
}

func parseTripBadgeLine(line string) (verifyJob, bool) {
	// Look for OSC8 trip link (only https)
	idx := strings.Index(line, "/api/trip/verify?")
	if idx == -1 {
		return verifyJob{}, false
	}
	// Find OSC8 start before idx
	start := strings.LastIndex(line[:idx], "\x1b]8;;")
	if start == -1 {
		return verifyJob{}, false
	}
	// Find URL end (ESC \ terminator)
	endRel := strings.Index(line[idx:], "\x1b\\")
	if endRel == -1 {
		return verifyJob{}, false
	}
	urlStr := line[idx : idx+endRel]
	// Badge is between first terminator and second OSC8
	firstTermEnd := idx + endRel + 2 // after \x1b\\
	secondOsc := strings.Index(line[firstTermEnd:], "\x1b]8;;")
	var badge string
	if secondOsc != -1 {
		badge = strings.TrimSpace(line[firstTermEnd : firstTermEnd+secondOsc])
		// Strip ANSI color if present (should be plain, but handle)
		// Badge is like "◆ ab12" possibly with color codes - strip them for hash
		// For now, badge as visible text without ANSI
		if idx2 := strings.Index(badge, "◆"); idx2 != -1 {
			badge = badge[idx2:]
			// Remove any ANSI inside badge (e.g., color prefix)
			if strings.Contains(badge, "\x1b[") {
				// Strip SGR codes for badge extraction
				badge = strings.TrimSpace(filter.SanitizeForDisplay(badge))
			}
		}
	} else {
		// Fallback: find ◆
		if p := strings.Index(line, "◆"); p != -1 {
			end := p + len("◆ ") + 8
			if end > len(line) {
				end = len(line)
			}
			badge = strings.TrimSpace(line[p:end])
		}
	}
	// Parse URL query to get pub/seq etc. Only https is supported now.
	fullURL := urlStr
	if !strings.HasPrefix(fullURL, "http") {
		fullURL = "https://" + fullURL
	}
	u, err := url.Parse(fullURL)
	if err != nil {
		return verifyJob{}, false
	}
	q := u.Query()
	job := verifyJob{
		rawLine:     line,
		badge:       badge,
		urlStr:      urlStr,
		pub:         q.Get("pub"),
		seqStr:      q.Get("seq"),
		prev:        q.Get("prev"),
		sig:         q.Get("sig"),
		msgHash:     q.Get("msg_hash"),
		serverPub:   q.Get("server_pub"),
		displayName: q.Get("display_name"),
		textParam:   q.Get("text"),
	}
	if job.pub == "" || job.sig == "" {
		return verifyJob{}, false
	}
	if v, err := strconv.ParseUint(job.seqStr, 10, 32); err == nil {
		job.seq = uint32(v)
	}
	if v, err := strconv.ParseUint(q.Get("tmp_id"), 10, 64); err == nil {
		job.tmpID = v
	}
	return job, true
}

func isTripBadgeLine(line string) bool {
	return strings.Contains(line, "◆") && strings.Contains(line, "/api/trip/verify")
}

func isJoinLeaveSystemLine(line string) bool {
	return strings.Contains(line, "[Hệ thống]:") && (strings.Contains(line, "đã tham gia") || strings.Contains(line, "đã rời"))
}

func isDateBannerLine(line string) bool {
	return strings.Contains(line, "--- Ngày ") && strings.Contains(line, " ---")
}

func isHistoryBoundaryLine(line string) bool {
	return strings.Contains(line, "--- Lịch sử chat gần đây ---") || strings.Contains(line, "--- Kết thúc lịch sử ---")
}

// collectCodeblock gathers a fenced code block after its opening line.
// It returns the joined text, or canceled=true when the user aborted with
// Ctrl+C or the stream ended — in which case the caller must discard
// everything and send nothing.
func collectCodeblock(term inputTerminal, firstLine string) (string, bool) {
	rawLines := []string{firstLine}

	term.SetPrompt("| ... ")
	defer term.SetPrompt("| > ")
	for {
		nextLine, err := term.ReadLine()
		if err != nil {
			return "", true
		}
		rawLines = append(rawLines, nextLine)

		if strings.HasSuffix(strings.TrimSpace(nextLine), "```") {
			break
		}
	}

	return strings.Join(rawLines, "\n"), false
}

func normalizeURL(input string) string {
	input = strings.TrimSpace(input)

	if !strings.HasPrefix(input, "http://") && !strings.HasPrefix(input, "https://") &&
		!strings.HasPrefix(input, "ws://") && !strings.HasPrefix(input, "wss://") {
		input = "wss://" + input
	}

	input = strings.Replace(input, "http://", "ws://", 1)
	input = strings.Replace(input, "https://", "wss://", 1)

	u, err := url.Parse(input)
	if err == nil {
		if u.Path == "" || u.Path == "/" {
			u.Path = "/ws"
		}
		return u.String()
	}

	return input
}

func checkServerInfo(input string) {
	input = strings.TrimSpace(input)

	if strings.HasPrefix(input, "ws://") {
		input = strings.Replace(input, "ws://", "http://", 1)
	} else if strings.HasPrefix(input, "wss://") {
		input = strings.Replace(input, "wss://", "https://", 1)
	} else if !strings.HasPrefix(input, "http://") && !strings.HasPrefix(input, "https://") {
		if strings.Contains(input, "localhost") || strings.HasPrefix(input, "127.") {
			input = "http://" + input
		} else {
			input = "https://" + input
		}
	}

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(input)
	if err != nil {
		fmt.Println("❌ Lỗi khi lấy thông tin:", err)
		return
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		fmt.Println("❌ Lỗi khi đọc dữ liệu:", err)
		return
	}

	fmt.Println("\n" + string(body))
}

func main() {
	parseFlags()

	if CLI.Info {
		checkServerInfo(CLI.Server)
		return
	}

	if CLI.Server == "" {
		fmt.Println("❌ Lỗi: Vui lòng cung cấp link server bằng cờ -s (VD: -s ws://localhost:8080)")
		return
	}

	wsURL := normalizeURL(CLI.Server)
	username := strings.TrimSpace(CLI.Username)

	conn, resp, err := dialWS(wsURL)
	if err != nil {
		// Auto-upgrade ws:// -> wss:// when server requires TLS (426)
		if resp != nil && resp.StatusCode == http.StatusUpgradeRequired && strings.HasPrefix(wsURL, "ws://") {
			wssURL := "wss://" + strings.TrimPrefix(wsURL, "ws://")
			fmt.Printf("🔒 Server yêu cầu wss://, đang thử lại với %s…\n", wssURL)
			if bodyBytes, _ := io.ReadAll(resp.Body); len(bodyBytes) > 0 {
				fmt.Printf("📦 Server: %s\n", strings.TrimSpace(string(bodyBytes)))
			}
			conn2, resp2, err2 := dialWS(wssURL)
			if err2 == nil {
				conn = conn2
				resp = resp2
				wsURL = wssURL
				err = nil
			} else {
				fmt.Printf("❌ Thử lại wss cũng thất bại: %v\n", err2)
				if resp2 != nil {
					fmt.Printf("👉 HTTP Status Code: %d\n", resp2.StatusCode)
				}
			}
		}
		if err != nil {
			fmt.Println("❌ Không thể kết nối:", err)
			if resp != nil {
				fmt.Printf("👉 HTTP Status Code: %d\n", resp.StatusCode)
				bodyBytes, _ := io.ReadAll(resp.Body)
				if len(bodyBytes) > 0 {
					fmt.Printf("📦 Nội dung phản hồi: %s\n", strings.TrimSpace(string(bodyBytes)))
				}
			}
			return
		}
	}
	defer conn.Close()

	var challenge AuthPacket
	err = conn.ReadJSON(&challenge)
	if err != nil || challenge.Type != "auth_challenge" {
		fmt.Println("❌ Lỗi: Server không gửi Auth Challenge hợp lệ.")
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
	var tmpSeq uint64
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
		Username: username,
		Nonce:    challenge.Nonce,
	}
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
		useEd25519, usePasskey := pickIdentity(idf)
		switch {
		case usePasskey:
			pk := idf.Passkey
			respPacket.Role = pk.Role
			credID, ad, cd, sig, aerr := pk.BuildAssertion(challenge.Nonce)
			if aerr != nil {
				fmt.Printf("❌ Passkey lỗi: %v\n", aerr)
				notifyQuit()
				return
			}
			respPacket.PasskeyID = credID
			respPacket.PasskeyAuthData = ad
			respPacket.PasskeyClientData = cd
			respPacket.PasskeySig = sig
			_ = SaveIdentityFileEncrypted(CLI.KeyFile, idf) // persist counter, keep encryption
			fmt.Printf("🔑 Đang yêu cầu cấp quyền bằng passkey: [%s]...\n", pk.Role)
		case useEd25519:
			id := idf.Ed25519
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
					fmt.Printf("🚨 Server identity mismatch! Pin %s != %s — abort.\n", id.ServerPubKey[:12], challenge.ServerPubKey[:12])
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
					fmt.Printf("⚠️ Lần đầu kết nối tới server %s pin %s…\n", challenge.ServerHost, challenge.ServerPubKey[:16])
				}
			}
			// Use server's pubkey for anti-reuse (instead of host string)
			bindValue := ""
			if challenge.ServerPubKey != "" {
				bindValue = challenge.ServerPubKey
			} else if id.ServerPubKey != "" {
				bindValue = id.ServerPubKey
			} else {
				if u, perr := url.Parse(wsURL); perr == nil {
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
		default:
			fmt.Println("❌ key.json không chứa danh tính nào.")
			notifyQuit()
			return
		}
	} else {
		// WebAuthn passkey login (web build only): failure already shown
		// via setWasmStatus; keep the runtime alive so late browser
		// callbacks (dialog dismissal, timers) don't hit a dead runtime.
		if !applyWebPasskey(&respPacket, challenge.Nonce) {
			conn.Close()
			parkForever()
			return
		}
	}

	err = conn.WriteJSON(respPacket)
	if err != nil {
		fmt.Println("❌ Lỗi gửi dữ liệu xác thực:", err)
		return
	}

	var authSuccess AuthPacket
	err = conn.ReadJSON(&authSuccess)
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
			conn.Close()
			parkForever()
		}
		notifyQuit()
		return
	case "auth_success":
		username = authSuccess.Username
	}

	term, err := newInputTerminal()
	if err != nil {
		fmt.Println("❌ Lỗi khởi tạo terminal:", err)
		return
	}
	defer term.Close()
	out := term.Writer()

	quitting := make(chan bool, 1)
	showJoinLeave := CLI.ShowJoin
	var showJoinMu sync.RWMutex

	greeting := func(w io.Writer, uname string) {
		fmt.Fprintln(w, "Đã kết nối với username:", uname)
		fmt.Fprint(w, "Gõ tin nhắn để chat, /help để hiện trợ giúp\n\n")
	}

	verifyCh := make(chan verifyJob, 128)
	var verifyMu sync.Mutex
	autoVerify := true
	var autoVerifyMu sync.RWMutex
	// showMeta toggles the trailing "#height:hash" line per session
	// (/meta, default on). The chain still verifies when hidden.
	showMeta := true
	var showMetaMu sync.RWMutex
	var displayMu sync.Mutex
	serverPubForVerify := challenge.ServerPubKey
	lastMessageTime := time.Now().Add(-10 * time.Second)

	activeTab := TabChat
	cl, cb, sl, sb := tabCaps()
	tabChat := newTabBuffer(cl, cb)
	tabSys := newTabBuffer(sl, sb)
	var printGen uint64

	// refreshCoalesced repaints at most every 100ms under burst; a
	// trailing timer guarantees the last update is never swallowed, so
	// single messages still appear instantly.
	var refreshMu sync.Mutex
	var lastRefresh time.Time
	var refreshPending bool
	refreshCoalesced := func() {
		refreshMu.Lock()
		if time.Since(lastRefresh) >= 100*time.Millisecond {
			lastRefresh = time.Now()
			refreshMu.Unlock()
			term.Refresh()
			return
		}
		if !refreshPending {
			refreshPending = true
			time.AfterFunc(100*time.Millisecond, func() {
				refreshMu.Lock()
				refreshPending = false
				lastRefresh = time.Now()
				refreshMu.Unlock()
				term.Refresh()
			})
		}
		refreshMu.Unlock()
	}

	// pendingPlaceholders tracks grey placeholders awaiting server echo
	// (pendingMsg lives at package level in chainmeta.go for tests).
	pendingPlaceholders := []pendingMsg{}
	// pendingEchoes buffers our echoes that arrived before their
	// placeholder was tracked (sub-millisecond local echo race).
	var pendingEchoes []pendingEcho

	// emitTab buffers a rendered line and prints it only when its tab is
	// active. Caller must hold displayMu.
	emitTab := func(tab int, line string) {
		if tab == TabChat {
			tabChat.append(line)
		} else {
			tabSys.append(line)
		}
		// Tab 1 shows the full legacy stream, so it is unaffected by tabs.
		// Tab 2 is purely additive and shows only its own lines.
		if tab == activeTab || activeTab == TabChat {
			fmt.Fprint(out, line)
			printGen++
		}
	}

	// emitLocalFeedback buffers a local command response in TabSystem and
	// prints it on the active tab immediately, so commands always respond
	// visibly while staying reviewable in Tab 2. Caller must hold displayMu.
	emitLocalFeedback := func(line string) {
		tabSys.append(line)
		fmt.Fprint(out, line)
		printGen++
	}

	// erasePlaceholderLocked splices a placeholder block out of the tab
	// buffer and rewrites the screen region, reprinting any lines that
	// intervened after it. Caller must hold displayMu.
	erasePlaceholderLocked := func(pm pendingMsg) {
		if !pm.shown || activeTab != TabChat || pm.rows <= 0 {
			return
		}
		start := pm.bufEnd - pm.rows
		if start < 0 || pm.bufEnd > len(tabChat.lines) {
			return
		}
		// Verify the region still holds our placeholder (eviction or
		// concurrent appends may have shifted it); otherwise leave the
		// screen alone and render the echo normally.
		for _, l := range tabChat.lines[start:pm.bufEnd] {
			if !strings.Contains(l, "⏳") {
				return
			}
		}
		intervening := append([]string{}, tabChat.lines[pm.bufEnd:]...)
		tabChat.spliceOut(start, pm.bufEnd)
		// Rows on screen: placeholder block plus intervening lines printed after it.
		fmt.Fprintf(out, "\x1b[%dA", pm.rows+len(intervening))
		fmt.Fprint(out, "\x1b[J")
		for _, l := range intervening {
			fmt.Fprint(out, l)
		}
		printGen++
	}

	// consumeEchoLocked matches a server echo of our own message against
	// pending placeholders by exact tmp_id (duplicate texts stay
	// unambiguous), with the legacy oldest-text match as fallback. A match
	// drops the entry and erases the placeholder. An echo from us that
	// matches nothing is stashed: it may have beaten its placeholder
	// (local echo race) and is retried when placeholders are tracked;
	// entries never matched expire with a warning, which is how
	// server-side ID tampering surfaces. Caller must hold displayMu.
	consumeEchoLocked := func(wire WireMessage) {
		var stale []WireMessage
		pendingEchoes, stale = reapStaleEchoes(pendingEchoes, 10*time.Second)
		for _, w := range stale {
			emitLocalFeedback(fmt.Sprintf("| [Local]: Echo không khớp tin đang chờ (tmp_id=%d) — ID có thể đã bị sửa.\n", w.TmpID))
		}
		if wire.DisplayName != username || len(pendingPlaceholders) == 0 {
			if wire.DisplayName == username && wire.TmpID != 0 {
				pendingEchoes = stashEcho(pendingEchoes, wire, 16)
			}
			return
		}
		idx := matchPendingIndex(pendingPlaceholders, wire.TmpID, wire.Text, username, wire.DisplayName)
		if idx == -1 {
			if wire.TmpID != 0 {
				pendingEchoes = stashEcho(pendingEchoes, wire, 16)
			}
			return
		}
		pm := pendingPlaceholders[idx]
		pendingPlaceholders = append(pendingPlaceholders[:idx], pendingPlaceholders[idx+1:]...)
		erasePlaceholderLocked(pm)
	}

	// Chain verification state: running tip adopted from the first
	// chained message seen, persisted tip for fork detection after sync.
	var chainTip [32]byte
	var chainHeight uint64
	var chainHaveTip bool
	var chainWarned bool
	tipPath := chainTipFile(historyFile)
	persistedTip, persistedHeight, havePersistedTip := loadChainTip(tipPath)
	inSync := false
	syncHashes := map[string]bool{}
	var syncFirstHeight uint64

	// noteChainTip advances the running tip, persisting it in batches:
	// every tipBatchSaves links plus explicit flushes (quit, fork warn).
	// A crash between batches only degrades the next sync to first-run
	// (no fork check), never to a false warning.
	const tipBatchSaves = 50
	var tipSinceSave uint64
	noteChainTip := func(tip [32]byte, height uint64) {
		chainTip, chainHeight, chainHaveTip = tip, height, true
		tipSinceSave++
		if tipSinceSave >= tipBatchSaves {
			saveChainTip(tipPath, tip, height)
			tipSinceSave = 0
		}
	}
	flushChainTip := func() {
		if chainHaveTip {
			saveChainTip(tipPath, chainTip, chainHeight)
			tipSinceSave = 0
		}
	}

	// checkChainLink verifies one received wire against the running tip:
	// content hash always, prev continuity once a tip is adopted. Legacy
	// lines without chain fields pass silently. The first chained message
	// adopts its own prev. Any break warns once and adopts (availability),
	// so chat stays usable while tampering stays visible. During history
	// sync every chained hash is collected for the fork check at the end
	// marker. Caller must hold displayMu (warns via local feedback).
	checkChainLink := func(wire WireMessage) {
		if wire.ChainHash == "" {
			return
		}
		if inSync {
			syncHashes[strings.ToLower(wire.ChainHash)] = true
			if syncFirstHeight == 0 {
				syncFirstHeight = wire.ChainHeight
			}
		}
		newTip, err := verifyWireLink(wire, chainTip)
		if err != nil && !chainHaveTip {
			// No tip yet: adopt the message's own prev, content-check only.
			prev, ok := chain.ParseHex64(wire.ChainPrev)
			if !ok {
				if !chainWarned {
					chainWarned = true
					emitLocalFeedback("| [Local]: Chain link đầu tiên sai định dạng — bỏ qua kiểm tra.\n")
				}
				return
			}
			newTip, err = verifyWireLink(wire, prev)
		}
		if err != nil {
			if !chainWarned {
				chainWarned = true
				emitLocalFeedback(fmt.Sprintf("| [Local]: Chuỗi tin bị đứt ở #%d (%v) — server hoặc lịch sử có thể đã bị sửa.\n", wire.ChainHeight, err))
			}
			if parsed, ok := chain.ParseHex64(wire.ChainHash); ok {
				noteChainTip(parsed, wire.ChainHeight)
			}
			flushChainTip()
			return
		}
		noteChainTip(newTip, wire.ChainHeight)
	}

	// badgeForWire verifies a trip badge and builds its colored display
	// plus the manual-verify hyperlink (kept, opens the stateless API).
	// av selects verified vs plain rendering; it is part of the render
	// cache key, so toggling /autoverify never serves stale colors.
	badgeForWire := func(wire WireMessage, av bool) (colored, urlStr string) {
		h := sha256.Sum256([]byte(wire.Trip.Pub))
		plain := "◆ " + hex.EncodeToString(h[:])[:8]
		if av {
			res, err := trip.Verify(trip.VerifyParams{
				Text:        wire.Text,
				DisplayName: wire.DisplayName,
				ServerPub:   wire.Trip.ServerPub,
				PubHex:      wire.Trip.Pub,
				Seq:         wire.Trip.Seq,
				PrevHex:     wire.Trip.Prev,
				SigHex:      wire.Trip.Sig,
				MsgHashHex:  wire.Trip.MsgHash,
				TmpID:       wire.Trip.TmpID,
			})
			if err == nil && res != nil {
				colored = badgeColor(res.Badge) + res.Badge + "\x1b[0m"
			} else {
				colored = "\x1b[91m" + plain + " ✗\x1b[0m"
			}
		} else {
			colored = plain
		}
		if u, err := url.Parse(wsURL); err == nil && u.Host != "" {
			urlStr = fmt.Sprintf("https://%s/api/trip/verify?pub=%s&seq=%d&prev=%s&sig=%s&msg_hash=%s&server_pub=%s&display_name=%s&text=%s&tmp_id=%d", u.Host, wire.Trip.Pub, wire.Trip.Seq, wire.Trip.Prev, wire.Trip.Sig, wire.Trip.MsgHash, wire.Trip.ServerPub, url.QueryEscape(wire.DisplayName), url.QueryEscape(wire.Text), wire.Trip.TmpID)
		}
		return colored, urlStr
	}

	// renderChatBlock renders one wire message as content rows plus exactly
	// one trailing meta line ("  └─  #height:hash | ✍️ badge"). Legacy
	// lines without chain fields render content only. System wires render
	// sanitized text to their classified tab. Caller must hold displayMu.
	// buildChatBlock renders one wire into head + trailing meta strings
	// without emitting. Pure given (wire, av, withMeta); cached by renderChatBlock.
	buildChatBlock := func(wire WireMessage, av, withMeta bool) (head, meta string, tab int, hasMeta bool) {
		tab = TabChat
		if wire.Type == "system" {
			head = fmt.Sprintf("| %s\n", filter.SanitizeForDisplay(wire.Text))
			tab = classifyTab(wire.Text)
		} else {
			head = fmt.Sprintf("| %s %s: %s\n", wire.Time, wire.DisplayName, renderChatText(wire.Text))
		}
		if wire.ChainHash == "" || !withMeta {
			return head, "", tab, false
		}
		meta = metaLineFor(wire.ChainHeight, wire.ChainHash, "")
		if wire.Trip != nil {
			colored, urlStr := badgeForWire(wire, av)
			badge := colored
			if urlStr != "" {
				badge = fmt.Sprintf("\x1b]8;;%s\x1b\\%s\x1b]8;;\x1b\\", urlStr, colored)
			}
			meta = metaLineFor(wire.ChainHeight, wire.ChainHash, badge)
		}
		return head, fmt.Sprintf("| %s\n", meta), tab, true
	}

	renderCache := newRenderCache(200)
	renderChatBlock := func(wire WireMessage) {
		autoVerifyMu.RLock()
		av := autoVerify
		autoVerifyMu.RUnlock()
		showMetaMu.RLock()
		withMeta := showMeta
		showMetaMu.RUnlock()
		// Replay and tab switches re-render the same immutable wires;
		// the chain hash covers the content, so it is a safe cache key
		// (verify mode and meta visibility are folded in).
		if wire.ChainHash != "" {
			key := strings.ToLower(wire.ChainHash) + "\x00" + wire.Type +
				"\x00" + map[bool]string{true: "v", false: "p"}[av] +
				"\x00" + map[bool]string{true: "m", false: "n"}[withMeta]
			if hit, ok := renderCache.get(key); ok {
				emitTab(hit.tab, hit.head)
				if hit.hasMeta {
					emitTab(hit.tab, hit.meta)
				}
				return
			}
			head, meta, tab, hasMeta := buildChatBlock(wire, av, withMeta)
			renderCache.put(key, renderedBlock{tab: tab, head: head, meta: meta, hasMeta: hasMeta})
			emitTab(tab, head)
			if hasMeta {
				emitTab(tab, meta)
			}
			return
		}
		head, meta, tab, hasMeta := buildChatBlock(wire, av, withMeta)
		emitTab(tab, head)
		if hasMeta {
			emitTab(tab, meta)
		}
	}

	// switchTab replays the target buffer under a single lock.
	switchTab := func(n int) {
		if n != TabChat && n != TabSystem {
			return
		}
		displayMu.Lock()
		defer displayMu.Unlock()
		if n == activeTab {
			return
		}
		activeTab = n
		printGen++
		fmt.Fprint(out, "\033[H\033[2J")
		var buf *tabBuffer
		if n == TabChat {
			buf = tabChat
		} else {
			buf = tabSys
		}
		var sb strings.Builder
		for _, l := range buf.lines {
			sb.WriteString(l)
		}
		fmt.Fprint(out, sb.String())
		term.Refresh()
	}

	enqueueVerify := func(job verifyJob) {
		verifyMu.Lock()
		defer verifyMu.Unlock()
		select {
		case verifyCh <- job:
		default:
			// Drop oldest (FIFO) — dropped is considered verify fail (red ✗)
			select {
			case <-verifyCh:
			default:
			}
			// Now space is guaranteed (or channel was emptied)
			select {
			case verifyCh <- job:
			default:
				// Extremely unlikely: channel filled again between drop and send
			}
		}
	}

	go func() {
		for job := range verifyCh {
			// Use shared trip verification (same as server) — serverPub is enforced to server's own key
			serverPub := strings.ToLower(job.serverPub)
			if serverPub == "" {
				serverPub = strings.ToLower(serverPubForVerify)
			}
			textForVerify := job.textParam
			// If textParam empty, we still verify via msgHash (trip.Verify recomputes)
			_, err := trip.Verify(trip.VerifyParams{
				Text:        textForVerify,
				DisplayName: job.displayName,
				ServerPub:   serverPub,
				PubHex:      job.pub,
				Seq:         job.seq,
				PrevHex:     job.prev,
				SigHex:      job.sig,
				MsgHashHex:  job.msgHash,
				TmpID:       job.tmpID,
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
			displayMu.Lock()
			emitTab(TabChat, fmt.Sprintf("| %s\n", filter.SanitizeForDisplay(line)))
			displayMu.Unlock()
			refreshCoalesced()
		}
	}()

	go func() {
		var pendingDateBanner string
		var pendingDateBannerWire *WireMessage
		// flushDateBannerLocked prints a stashed date banner to TabSystem
		// before the block that follows it. Caller must hold displayMu.
		flushDateBannerLocked := func() {
			if pendingDateBanner != "" {
				emitTab(TabSystem, fmt.Sprintf("| %s\n", filter.SanitizeForDisplay(pendingDateBanner)))
				pendingDateBanner = ""
			}
			if pendingDateBannerWire != nil {
				renderChatBlock(*pendingDateBannerWire)
				pendingDateBannerWire = nil
			}
		};
		for {
			_, msg, err := conn.ReadMessage()
			if err != nil {
				select {
				case <-quitting:
					return
				default:
					fmt.Fprintf(out, "\r\033[K\n ❌ Mất kết nối server\n")
					os.Exit(1)
				}
			}

			showJoinMu.RLock()
			isShowingJoin := showJoinLeave
			showJoinMu.RUnlock()

			// Try to handle structured WireMessage JSON first (for new protocol)
			var wire WireMessage
			if err := json.Unmarshal(msg, &wire); err == nil && wire.Type == "chat" {
				displayMu.Lock()
				consumeEchoLocked(wire)
				checkChainLink(wire)
				flushDateBannerLocked()
				renderChatBlock(wire)
				displayMu.Unlock()
				refreshCoalesced()
				continue
			}
			var sysWire WireMessage
			if err := json.Unmarshal(msg, &sysWire); err == nil && sysWire.Type == "system" {
				displayMu.Lock()
				checkChainLink(sysWire)
				if !isShowingJoin && isDateBannerLine(sysWire.Text) {
					pendingDateBannerWire = &sysWire
					displayMu.Unlock()
					refreshCoalesced()
					continue
				}
				if !isShowingJoin && isJoinLeaveSystemLine(sysWire.Text) {
					displayMu.Unlock()
					refreshCoalesced()
					continue
				}
				flushDateBannerLocked()
				renderChatBlock(sysWire)
				displayMu.Unlock()
				refreshCoalesced()
				continue
			}
			lines := strings.Split(string(msg), "\n")
			for _, line := range lines {
				// Also try per-line JSON (for history blob where each line is a WireMessage JSON)
				var wl WireMessage
				if err := json.Unmarshal([]byte(line), &wl); err == nil && (wl.Type == "chat" || wl.Type == "system") {
					displayMu.Lock()
					if wl.Type == "chat" {
						consumeEchoLocked(wl)
					}
					checkChainLink(wl)
					if wl.Type == "system" && !isShowingJoin && isDateBannerLine(wl.Text) {
						pendingDateBannerWire = &wl
						displayMu.Unlock()
						continue
					}
					if wl.Type == "system" && !isShowingJoin && isJoinLeaveSystemLine(wl.Text) {
						displayMu.Unlock()
						continue
					}
					flushDateBannerLocked()
					renderChatBlock(wl)
					displayMu.Unlock()
					continue
				}
				if !isShowingJoin && isDateBannerLine(line) {
					pendingDateBanner = line
					continue
				}
				if !isShowingJoin && isJoinLeaveSystemLine(line) {
					continue
				}
				if !isShowingJoin && isHistoryBoundaryLine(line) {
					displayMu.Lock()
					if strings.Contains(line, "--- Lịch sử chat gần đây ---") {
						inSync = true
						syncHashes = map[string]bool{}
						syncFirstHeight = 0
					}
					if strings.Contains(line, "--- Kết thúc lịch sử ---") {
						pendingDateBanner = ""
						pendingDateBannerWire = nil
						inSync = false
						if havePersistedTip && len(syncHashes) > 0 {
							tipHex := strings.ToLower(hex.EncodeToString(persistedTip[:]))
							if !syncHashes[tipHex] && persistedHeight >= syncFirstHeight && syncFirstHeight > 0 {
								emitLocalFeedback("| [Local]: Lịch sử server không chứa tip đã lưu — log có thể đã phân nhánh (fork).\n")
								flushChainTip()
							}
						}
					}
					emitTab(TabChat, fmt.Sprintf("| %s\n", filter.SanitizeForDisplay(line)))
					displayMu.Unlock()
					continue
				}
				if !isShowingJoin && (pendingDateBanner != "" || pendingDateBannerWire != nil) {
					displayMu.Lock()
					flushDateBannerLocked()
					displayMu.Unlock()
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
							enqueueVerify(job)
							continue
						}
					}
				}
				displayMu.Lock()
				emitTab(classifyTab(line), fmt.Sprintf("| %s\n", filter.SanitizeForDisplay(line)))
				displayMu.Unlock()
			}
			refreshCoalesced()
		}
	}()

	greeting(out, username)

	// gracefulQuit closes the connection cleanly like /quit does, so both
	// an explicit quit command and an EOF (Ctrl+D) leave no dangling state.
	gracefulQuit := func() {
		quitting <- true
		flushChainTip()
		conn.WriteMessage(wsCloseMessage, []byte{})
		// Zero trip private key
		if tripPriv != nil {
			for i := range tripPriv {
				tripPriv[i] = 0
			}
		}
		fmt.Fprintf(out, "👋 Đang ngắt kết nối... Tạm biệt!\n")
		time.Sleep(500 * time.Millisecond)
		notifyQuit()
	}

	for {
		text, err := term.ReadLine()
		if err != nil {
			if errors.Is(err, ErrInputCancel) {
				term.SetPrompt("| > ")
				displayMu.Lock()
				emitLocalFeedback("| [Local]: Ctrl+C chỉ hủy dòng nhập, thoát app bằng Ctrl+D.\n")
				displayMu.Unlock()
				term.Refresh()
				continue
			}
			gracefulQuit()
			break
		}

		text = strings.TrimSpace(text)
		if text == "" {
			continue
		}

		if text == "/quit" || text == "/q" {
			gracefulQuit()
			break
		}

		if text == "/whoami" || text == "/w" {
			displayMu.Lock()
			emitLocalFeedback(fmt.Sprintf("| [Local]: Người dùng: %s | Xác thực: %s\n", username, sessAuthType))
			if sessRole != "" {
				emitLocalFeedback(fmt.Sprintf("| [Local]: Role: %s | Unlimited: %v | Prefix: %q\n", sessRole, sessUnlimited, sessPrefix))
				displayMu.Unlock()
			}
			continue
		}

		if text == "/status" {
			showJoinMu.RLock()
			sj := "TẮT"
			if showJoinLeave {
				sj = "BẬT"
			}
			showJoinMu.RUnlock()
			displayMu.Lock()
			emitLocalFeedback(fmt.Sprintf("| [Local]: Server: %s | Đã kết nối: %s | Phiên bản: %s | Show-join: %s\n",
				wsURL, time.Since(sessConnected).Round(time.Second), Version, sj))
			displayMu.Unlock()
			continue
		}

		if text == "/help" || text == "/h" {
			displayMu.Lock()
			emitLocalFeedback("  [Trợ giúp]: Danh sách các lệnh có thể sử dụng:\n")
			emitLocalFeedback("    - /help, /h      : Hiển thị bảng trợ giúp này\n")
			emitLocalFeedback("    - /clear, /c     : Xóa sạch màn hình chat\n")
			emitLocalFeedback("    - /clearhistory, /ch: Xóa file lịch sử gõ phím lưu trên máy\n")
			emitLocalFeedback("    - /quit, /q      : Rời phòng chat và tắt ứng dụng\n")
			emitLocalFeedback("    - /showjoin, /sj : Bật/tắt hiện thông báo người khác ra vào phòng cho các tin kế tiếp\n")
			emitLocalFeedback("    - /whoami, /w    : Thông tin danh tính và quyền hiện tại\n")
			emitLocalFeedback("    - /status        : Trạng thái kết nối và phiên bản client\n")
			emitLocalFeedback("    - /autoverify, /av: Bật/tắt auto-verify trip (mặc định BẬT, queue FIFO, verify song song)\n")
			emitLocalFeedback("    - /verify        : Hướng dẫn verify thủ công qua link API\n")
			emitLocalFeedback("    - /tab, /t [1|2]  : Chuyển tab chat / local & system\n")
			emitLocalFeedback("    - /meta, /m [on|off]: Hiện/ẩn dòng meta #height:hash (mặc định hiện, chain vẫn verify)\n")
			emitLocalFeedback("    - /find, /f <n>[:hash]: Tìm tin theo số height trong bộ nhớ (vd /find 1234)\n")
			emitLocalFeedback("    - Lệnh lạ bắt đầu bằng / bị chặn, không gửi đi (muốn gửi chữ / đầu dòng thì dùng codeblock)\n")
			emitLocalFeedback("    - Gõ ``` ở đầu và cuối tin nhắn để gửi Code block / nhiều dòng (^C hủy nhập)\n")
			emitLocalFeedback("    - Bọc chữ trong `dấu backtick` để hiện nền riêng (inline code một dòng)\n")
			displayMu.Unlock()
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
			displayMu.Lock()
			emitLocalFeedback(fmt.Sprintf("| [Local]: %s hiển thị thông báo người dùng ra/vào phòng cho các tin kế tiếp.\n", status))
			displayMu.Unlock()
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
			displayMu.Lock()
			emitLocalFeedback(fmt.Sprintf("| [Local]: Auto-verify đã %s (mặc định BẬT, verify song song qua channel FIFO).\n", status))
			displayMu.Unlock()
			continue
		}

		if strings.HasPrefix(text, "/verify") {
			displayMu.Lock()
			emitLocalFeedback("  [Verify]: Click badge link (https://.../api/trip/verify?...) để verify thủ công qua API stateless.\n")
			displayMu.Unlock()
			continue
		}

		if text == "/tab" || text == "/t" || strings.HasPrefix(text, "/tab ") || strings.HasPrefix(text, "/t ") {
			n := activeTab
			if text == "/tab" || text == "/t" {
				if activeTab == TabChat {
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
			switchTab(n)
			displayMu.Lock()
			emitLocalFeedback(tabBarLine(activeTab))
			printGen++
			displayMu.Unlock()
			continue
		}

		if text == "/clear" || text == "/c" {
			fmt.Fprint(out, "\033[H\033[2J")
			greeting(out, username)
			continue
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
				displayMu.Lock()
				emitLocalFeedback("| [Local]: Dùng /meta, /meta on hoặc /meta off.\n")
				displayMu.Unlock()
				term.Refresh()
				continue
			}
			state := "HIỆN"
			if !showMeta {
				state = "ẨN"
			}
			showMetaMu.Unlock()
			displayMu.Lock()
			emitLocalFeedback(fmt.Sprintf("| [Local]: Dòng meta (#height:hash) %s (chain vẫn verify ngầm).\n", state))
			displayMu.Unlock()
			term.Refresh()
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
				displayMu.Lock()
				emitLocalFeedback(fmt.Sprintf("| [Local]: %v.\n", err))
				displayMu.Unlock()
				term.Refresh()
				continue
			}
			displayMu.Lock()
			shown := 0
			fmt.Fprintf(out, "| [Local]: Tìm #%d", height)
			if suffix != "" {
				fmt.Fprintf(out, ":%s", suffix)
			}
			fmt.Fprintf(out, " trong bộ nhớ:\n")
			printGen++
			for _, buf := range []*tabBuffer{tabChat, tabSys} {
				matches := findMetaMatches(buf.lines, height, suffix)
				for _, idx := range matches {
					if idx > 0 {
						fmt.Fprint(out, buf.lines[idx-1])
						printGen++
					}
					fmt.Fprint(out, buf.lines[idx])
					printGen++
				}
				shown += len(matches)
			}
			if shown == 0 {
				fmt.Fprintf(out, "| [Local]: Không thấy (tin cũ đã bị evict khỏi bộ nhớ hoặc chưa sync).\n")
				printGen++
			}
			displayMu.Unlock()
			term.Refresh()
			continue
		}

		if text == "/clearhistory" || text == "/ch" {
			os.Remove(historyFile)
			displayMu.Lock()
			fmt.Fprintf(out, "🗑️ Đã xóa file lịch sử gõ phím tại: %s\n", historyFile)
			printGen++
			displayMu.Unlock()
			continue
		}

		// Unknown slash input: every built-in command was already matched
		// above, so anything still starting with "/" is a mistyped command
		// rejected locally and never broadcast. Known commands above
		// already continued.
		if isUnknownSlashCommand(text) {
			displayMu.Lock()
			emitLocalFeedback(fmt.Sprintf("| [Local]: Lệnh không tồn tại: %s. Gõ /help để xem danh sách.\n", text))
			displayMu.Unlock()
			term.Refresh()
			continue
		}

		typedLinesCount := 1

		if strings.HasPrefix(text, "```") {
			if !codebg.NeedsContinuation(text) {
				// Single-line fence (```code```): complete already.
				typedLinesCount = 1
			} else {
				var canceled bool
				text, canceled = collectCodeblock(term, text)
				if canceled {
					continue
				}
				typedLinesCount = strings.Count(text, "\n") + 1
			}
		}

		if err := filter.ValidateMessage(text); err != nil {
			displayMu.Lock()
			emitLocalFeedback(fmt.Sprintf("| [Local]: Tin nhắn chứa ký tự không hợp lệ và đã bị chặn (client-side): %v\n", err))
			displayMu.Unlock()
			term.Refresh()
			continue
		}

		// Guard: client-side MessageCooldown (mirror server, zero-trust)
		if ClientCfg != nil {
			if err := guard.ValidateMessageForSend(text, lastMessageTime, &ClientCfg.Limits, false); err != nil {
				if err == guard.ErrTooFast {
					displayMu.Lock()
					emitLocalFeedback(fmt.Sprintf("| [Local]: Bạn đang chat quá nhanh! Vui lòng đợi %v.\n", ClientCfg.Limits.MessageCooldown))
					displayMu.Unlock()
					term.Refresh()
					continue
				}
				if err == guard.ErrTooLong {
					displayMu.Lock()
					emitLocalFeedback(fmt.Sprintf("| [Local]: Tin nhắn quá dài (tối đa %d ký tự).\n", ClientCfg.Limits.MaxMessageLength))
					displayMu.Unlock()
					term.Refresh()
					continue
				}
			}
		}

		// Placeholder: keep original text grey with pending indicator until server echo
		// Single displayMu lock for entire wipe + placeholder + trip sign + send to avoid burst drift
		phRows := 0
		phShown := activeTab == TabChat
		phBufEnd := 0
		displayMu.Lock()
		for range typedLinesCount {
			fmt.Fprint(out, "\033[1A\033[2K\r")
		}

		// Render code spans on the whole text first so fenced blocks
		// keep their state across lines; phRows then counts rendered
		// rows (headers added, closers dropped), matching the erase math.
		// Plain Render (no highlight): highlight's full resets would
		// cancel the grey placeholder wrapper mid-line, and line counts
		// match the highlighted echo anyway.
		lines := strings.Split(codebg.Render(text), "\n")
		phRows = len(lines)
		for i, line := range lines {
			line = linkify.Linkify(line)
			if i == 0 {
				emitTab(TabChat, fmt.Sprintf("\x1b[90m| Bạn: %s ⏳\x1b[0m\n", line))
			} else {
				emitTab(TabChat, fmt.Sprintf("\x1b[90m|      %s\x1b[0m\n", line))
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
				emitTab(TabChat, fmt.Sprintf("\x1b[90m|  └─ ✍ ◆ %s ⏳\x1b[0m\n", badgePlaceholder))
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
			emitTab(TabChat, "\x1b[90m|   └─  ··· ⏳\x1b[0m\n")
			phRows++
		}
		phBufEnd = len(tabChat.lines)
		term.Refresh()
		displayMu.Unlock()

		tmpSeq++
		if tripPriv != nil {
			// Sign message with trip chain — bind displayName for anti-spoof
			tripSeq++
			msgHash := sha256.Sum256([]byte(text))
			prevCopy := make([]byte, len(tripPrev))
			copy(prevCopy, tripPrev)
			payload := canonicalPayload(strings.ToLower(challenge.ServerPubKey), tripSeq, prevCopy, msgHash[:], []byte(tripPub), username, tmpSeq)
			sig := ed25519.Sign(tripPriv, payload)
			h := sha256.New()
			h.Write(prevCopy)
			h.Write(sig)
			h.Write(msgHash[:])
			newPrev := h.Sum(nil)
			copy(tripPrev, newPrev)
			tripMsg := TripMessage{Text: text, Pub: hex.EncodeToString([]byte(tripPub)), Seq: tripSeq, Prev: hex.EncodeToString(prevCopy), Sig: hex.EncodeToString(sig), DisplayName: username, TmpID: tmpSeq}
			err = conn.WriteJSON(tripMsg)
			if err != nil {
				// Rollback seq/prev on send failure to avoid permanent fork
				tripSeq--
				copy(tripPrev, prevCopy)
				tmpSeq--
			}
		} else {
			// Unsigned chat always travels in an envelope carrying the
			// session counter; raw text is rejected by the server.
			err = conn.WriteJSON(PlainMessage{TmpID: tmpSeq, Text: text})
			if err != nil {
				tmpSeq--
			}
		}
		if err != nil {
			// Mark placeholder as failed (red) is handled by server unicast; keep placeholder grey until then
			lastMessageTime = time.Now()
		} else {
			lastMessageTime = time.Now()
			// Track placeholder so the server echo can replace it.
			displayMu.Lock()
			pm := pendingMsg{text: text, rows: phRows, shown: phShown, gen: printGen, bufEnd: phBufEnd, sentAt: time.Now(), tmpID: tmpSeq}
			if tripPriv != nil {
				pm.hasTrip = true
				pm.seq = tripSeq
				pm.pub = hex.EncodeToString([]byte(tripPub))
			}
			pendingPlaceholders = append(pendingPlaceholders, pm)
			// Bound the queue: echoes that never arrive (dead server, old
			// build) must not grow memory or turn matching quadratic.
			// Evicted entries stay grey on screen: honestly unconfirmed.
			// Linear scan stays trivial at this bound, so no index map.
			const maxPendingPlaceholders = 128
			for len(pendingPlaceholders) > maxPendingPlaceholders {
				pendingPlaceholders = pendingPlaceholders[1:]
			}
			// The echo may have beaten us here (local echo race): if a
			// stashed echo matches, erase the placeholder at once. The
			// echo itself was already rendered when it arrived.
			var haveStashed bool
			pendingEchoes, _, haveStashed = takeStashedEcho(pendingEchoes, pm.tmpID)
			if haveStashed {
				for i, p := range pendingPlaceholders {
					if p.tmpID == pm.tmpID {
						pendingPlaceholders = append(pendingPlaceholders[:i], pendingPlaceholders[i+1:]...)
						break
					}
				}
				erasePlaceholderLocked(pm)
			}
			displayMu.Unlock()
		}
		if err != nil {
			fmt.Println("❌ Lỗi gửi tin nhắn:", err)
			break
		}

	}
}

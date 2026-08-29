package main

import (
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"localchat/internal/filter"
	"localchat/linkify"

	"github.com/alecthomas/kong"
)

var Version = "dev"

var CLI struct {
	Version   kong.VersionFlag `help:"Hiển thị phiên bản (Git Commit Hash)" short:"v"`
	Server    string           `help:"Link server WebSocket" short:"s"`
	Username  string           `help:"Tên người dùng của bạn" default:"Anonymous" short:"u"`
	Tripcode  string           `help:"Mật khẩu bí mật để tạo Chữ ký Tripcode (tùy chọn)" short:"t"`
	UserAgent string           `help:"Tùy chỉnh User-Agent" default:"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36" short:"a"`
	Info      bool             `help:"Kiểm tra thông tin trạng thái của Server" short:"i"`
	ShowJoin  bool             `help:"Hiện thông báo người dùng ra/vào phòng" short:"j"`

	KeyFile string `help:"Đường dẫn file chứa khóa xác thực" short:"k"`
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

// historyFile is the readline history path used on the desktop build.
var historyFile = filepath.Join(os.TempDir(), "V2V_chat_history.tmp")

func isJoinLeaveSystemLine(line string) bool {
	return strings.Contains(line, "[Hệ thống]:") && (strings.Contains(line, "đã tham gia") || strings.Contains(line, "đã rời"))
}

func isDateBannerLine(line string) bool {
	return strings.Contains(line, "--- Ngày ") && strings.Contains(line, " ---")
}

func isHistoryBoundaryLine(line string) bool {
	return strings.Contains(line, "--- Lịch sử chat gần đây ---") || strings.Contains(line, "--- Kết thúc lịch sử ---")
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

	go func() {
		var pendingDateBanner string

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

			lines := strings.Split(string(msg), "\n")
			for _, line := range lines {
				if !isShowingJoin && isDateBannerLine(line) {
					pendingDateBanner = line
					continue
				}
				if !isShowingJoin && isJoinLeaveSystemLine(line) {
					continue
				}
				if !isShowingJoin && isHistoryBoundaryLine(line) {
					if strings.Contains(line, "--- Kết thúc lịch sử ---") {
						pendingDateBanner = ""
					}
					fmt.Fprintf(out, "| %s\n", filter.SanitizeForDisplay(line))
					continue
				}
				if !isShowingJoin && pendingDateBanner != "" {
					fmt.Fprintf(out, "| %s\n", filter.SanitizeForDisplay(pendingDateBanner))
					pendingDateBanner = ""
				}
				fmt.Fprintf(out, "| %s\n", filter.SanitizeForDisplay(line))
			}
			term.Refresh()
		}
	}()

	greeting(out, username)

	for {
		text, err := term.ReadLine()
		if err != nil {
			break
		}

		text = strings.TrimSpace(text)
		if text == "" {
			continue
		}

		if text == "/quit" || text == "/q" {
			quitting <- true
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
			break
		}

		if text == "/whoami" || text == "/w" {
			fmt.Fprintf(out, "| [Local]: Người dùng: %s | Xác thực: %s\n", username, sessAuthType)
			if sessRole != "" {
				fmt.Fprintf(out, "| [Local]: Role: %s | Unlimited: %v | Prefix: %q\n", sessRole, sessUnlimited, sessPrefix)
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
			fmt.Fprintf(out, "| [Local]: Server: %s | Đã kết nối: %s | Phiên bản: %s | Show-join: %s\n",
				wsURL, time.Since(sessConnected).Round(time.Second), Version, sj)
			continue
		}

		if text == "/help" || text == "/h" {
			fmt.Fprintln(out, "  [Trợ giúp]: Danh sách các lệnh có thể sử dụng:")
			fmt.Fprintln(out, "    - /help, /h      : Hiển thị bảng trợ giúp này")
			fmt.Fprintln(out, "    - /clear, /c     : Xóa sạch màn hình chat")
			fmt.Fprintln(out, "    - /clearhistory, /ch: Xóa file lịch sử gõ phím lưu trên máy")
			fmt.Fprintln(out, "    - /quit, /q      : Rời phòng chat và tắt ứng dụng")
			fmt.Fprintln(out, "    - /showjoin, /sj : Bật/tắt hiện thông báo người khác ra vào phòng cho các tin kế tiếp")
			fmt.Fprintln(out, "    - /whoami, /w    : Thông tin danh tính và quyền hiện tại")
			fmt.Fprintln(out, "    - /status        : Trạng thái kết nối và phiên bản client")
			fmt.Fprintln(out, "    - Gõ ``` ở đầu và cuối tin nhắn để gửi Code block / nhiều dòng")
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
			fmt.Fprintf(out, "| [Local]: %s hiển thị thông báo người dùng ra/vào phòng cho các tin kế tiếp.\n", status)
			continue
		}

		if text == "/clear" || text == "/c" {
			fmt.Fprint(out, "\033[H\033[2J")
			greeting(out, username)
			continue
		}

		if text == "/clearhistory" || text == "/ch" {
			os.Remove(historyFile)
			fmt.Fprintf(out, "🗑️ Đã xóa file lịch sử gõ phím tại: %s\n", historyFile)
			continue
		}

		typedLinesCount := 1

		if strings.HasPrefix(text, "```") {
			var rawLines []string
			rawLines = append(rawLines, text)

			term.SetPrompt("| ... ")
			for {
				nextLine, err := term.ReadLine()
				if err != nil {
					break
				}
				typedLinesCount++
				rawLines = append(rawLines, nextLine)

				if strings.HasSuffix(strings.TrimSpace(nextLine), "```") {
					break
				}
			}

			term.SetPrompt("| > ")
			text = strings.Join(rawLines, "\n")
		}

		if err := filter.ValidateMessage(text); err != nil {
			fmt.Fprintf(out, "| [Local]: Tin nhắn chứa ký tự không hợp lệ và đã bị chặn (client-side): %v\n", err)
			term.Refresh()
			continue
		}

		for range typedLinesCount {
			fmt.Fprint(out, "\033[1A\033[2K\r")
		}

		lines := strings.Split(text, "\n")
		for i, line := range lines {
			// Linkify after validation (linkify inserts ANSI)
			line = linkify.Linkify(line)
			if i == 0 {
				fmt.Fprintf(out, "| Bạn: %s\n", line)
			} else {
				fmt.Fprintf(out, "|      %s\n", line)
			}
		}

		if tripPriv != nil {
			// Sign message with trip chain
			tripSeq++
			msgHash := sha256.Sum256([]byte(text))
			prevCopy := make([]byte, len(tripPrev))
			copy(prevCopy, tripPrev)
			payload := canonicalPayload(challenge.ServerPubKey, tripSeq, prevCopy, msgHash[:], []byte(tripPub))
			sig := ed25519.Sign(tripPriv, payload)
			h := sha256.New()
			h.Write(prevCopy)
			h.Write(sig)
			h.Write(msgHash[:])
			newPrev := h.Sum(nil)
			copy(tripPrev, newPrev)
			fmt.Fprintf(out, "|  └─ ✍  ◆ %s\n", tripBadge)
			tripMsg := TripMessage{Text: text, Pub: hex.EncodeToString([]byte(tripPub)), Seq: tripSeq, Prev: hex.EncodeToString(prevCopy), Sig: hex.EncodeToString(sig)}
			err = conn.WriteJSON(tripMsg)
		} else if CLI.Tripcode != "" {
			hashTrip := sha256.Sum256([]byte(CLI.Tripcode))
			tripCodeHex := hex.EncodeToString(hashTrip[:])[:8]
			fmt.Fprintf(out, "|  └─ ✍  ◆ %s\n", tripCodeHex)
			err = conn.WriteMessage(wsTextMessage, []byte(text))
		} else {
			err = conn.WriteMessage(wsTextMessage, []byte(text))
		}
		if err != nil {
			fmt.Println("❌ Lỗi gửi tin nhắn:", err)
			break
		}

	}
}

package main

import (
	"bufio"
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/rand"
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
	GenKey  bool   `help:"Tạo identity mới (key file hoặc passkey mềm) và in cấu hình cho Server" short:"g"`
}

type AuthPacket struct {
	Type      string `json:"type"`
	Nonce     string `json:"nonce,omitempty"`
	Role      string `json:"role,omitempty"`
	Signature string `json:"signature,omitempty"`
	Hmac      string `json:"hmac,omitempty"`
	Username  string `json:"username,omitempty"`
	Tripcode  string `json:"tripcode,omitempty"`

	PasskeyID         string `json:"passkey_id,omitempty"`
	PasskeyAuthData   string `json:"passkey_auth_data,omitempty"`
	PasskeyClientData string `json:"passkey_client_data,omitempty"`
	PasskeySig        string `json:"passkey_sig,omitempty"`

	Error    string      `json:"error,omitempty"`
	AuthType string      `json:"auth_type,omitempty"`
	Perms    *Permission `json:"perms,omitempty"`
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

// keyFilePath is the local identity container shared by both flavors.
const keyFilePath = "key.json"

// rolesFilePath is where the generator merges role entries for the server.
const rolesFilePath = "roles.json"

// generateKeyInteractive creates a new role identity. Two flavors share the
// same flow, mirroring how roles.json accepts both: a classic ed25519 key
// file, or a software passkey in WebAuthn wire format. Each flavor owns one
// slot inside key.json — regenerating replaces its own slot and keeps the
// sibling intact.
func generateKeyInteractive() {
	reader := bufio.NewReader(os.Stdin)

	fmt.Print("Nhập tên role (Mặc định: admin): ")
	role, _ := reader.ReadString('\n')
	role = strings.TrimSpace(role)
	if role == "" {
		role = "admin"
	}

	fmt.Print("Loại danh tính — [1] key file ed25519 (mặc định)  [2] passkey mềm: ")
	kind, _ := reader.ReadString('\n')

	idf, err := LoadIdentityFile(keyFilePath)
	if err != nil && !os.IsNotExist(err) {
		// Unparseable container: refuse instead of destroying sibling slots.
		fmt.Println("❌", err)
		return
	}
	if idf == nil {
		idf = &IdentityFile{}
	}

	if strings.TrimSpace(kind) == "2" {
		generatePasskeySlot(reader, idf, role)
	} else {
		generateEd25519Slot(reader, idf, role)
	}

	if err := idf.Save(keyFilePath); err != nil {
		fmt.Println("❌ Lỗi lưu file key.json:", err)
		return
	}
	fmt.Println("\nĐã lưu: ./key.json (GIỮ BÍ MẬT FILE NÀY!)")
}

// generateEd25519Slot creates the classic key-file identity.
func generateEd25519Slot(reader *bufio.Reader, idf *IdentityFile, role string) {
	fmt.Printf("%s này có quyền chat không giới hạn? (Y/n) ", role)
	unlimitedStr, _ := reader.ReadString('\n')
	unlimitedStr = strings.TrimSpace(strings.ToLower(unlimitedStr))
	unlimited := true
	if unlimitedStr == "n" {
		unlimited = false
	}

	fmt.Print("Nhập Prefix hiển thị (Mặc định: \"[Admin] \"): ")
	prefix, _ := reader.ReadString('\n')
	prefix = strings.TrimSuffix(strings.TrimSuffix(prefix, "\n"), "\r")
	if prefix == "" {
		prefix = "[Admin] "
	}

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		fmt.Println("❌ Lỗi sinh khóa:", err)
		return
	}

	hmacBytes := make([]byte, 16)
	rand.Read(hmacBytes)
	hmacShield := hex.EncodeToString(hmacBytes)

	idf.Ed25519 = &Ed25519Identity{
		Role:       role,
		PrivateKey: hex.EncodeToString(priv),
		HmacShield: hmacShield,
	}

	err = mergeRolesFile(rolesFilePath, role, func(entry map[string]any) {
		entry["identities"] = []map[string]string{
			{
				"public_key":  hex.EncodeToString(pub),
				"hmac_shield": hmacShield,
			},
		}
		entry["can_message_unlimited"] = unlimited
		entry["custom_prefix"] = prefix
	})
	if err != nil {
		fmt.Println("❌", err)
		return
	}
	fmt.Println("Đã cập nhật ./roles.json")
}

// generatePasskeySlot creates the software passkey identity.
func generatePasskeySlot(reader *bufio.Reader, idf *IdentityFile, role string) {
	rpid := os.Getenv("WEBAUTHN_RPID")
	origin := os.Getenv("WEBAUTHN_ORIGIN")
	if rpid == "" {
		fmt.Print("WEBAUTHN_RPID chưa đặt — nhập RP ID (vd: chat.example.com): ")
		line, _ := reader.ReadString('\n')
		rpid = strings.TrimSpace(line)
	}
	if origin == "" {
		fmt.Print("WEBAUTHN_ORIGIN chưa đặt — nhập Origin (vd: https://chat.example.com): ")
		line, _ := reader.ReadString('\n')
		origin = strings.TrimSpace(line)
	}

	pk, err := GeneratePasskey(role, rpid, origin)
	if err != nil {
		fmt.Println("❌ Lỗi sinh passkey:", err)
		return
	}

	idf.Passkey = pk

	err = mergeRolesFile(rolesFilePath, role, func(entry map[string]any) {
		newEntry := map[string]any{
			"credential_id": pk.CredentialID,
			"public_key":    pk.PublicKey,
			"added_at":      timeNowRFC3339(),
		}
		list, _ := entry["passkeys"].([]any)
		for i, raw := range list {
			if ex, _ := raw.(map[string]any); ex != nil && ex["credential_id"] == pk.CredentialID {
				list[i] = newEntry
				return
			}
		}
		entry["passkeys"] = append(list, newEntry)
	})
	if err != nil {
		fmt.Println("❌", err)
		return
	}
	fmt.Println("Đã cập nhật ./roles.json")
}

func main() {
	parseFlags()

	if CLI.GenKey {
		generateKeyInteractive()
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

	wsURL := normalizeURL(CLI.Server)
	username := strings.TrimSpace(CLI.Username)

	conn, resp, err := dialWS(wsURL)
	if err != nil {
		fmt.Println("❌ Không thể kết nối:", err)
		if resp != nil {
			fmt.Printf("👉 HTTP Status Code: %d\n", resp.StatusCode)

			if resp.StatusCode == 200 {
				bodyBytes, _ := io.ReadAll(resp.Body)
				fmt.Printf("📦 Nội dung phản hồi: %s\n", string(bodyBytes))
			}
		}
		return
	}
	defer conn.Close()

	var challenge AuthPacket
	err = conn.ReadJSON(&challenge)
	if err != nil || challenge.Type != "auth_challenge" {
		fmt.Println("❌ Lỗi: Server không gửi Auth Challenge hợp lệ.")
		return
	}

	respPacket := AuthPacket{
		Username: username,
		Tripcode: CLI.Tripcode,
		Nonce:    challenge.Nonce,
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
			_ = idf.Save(CLI.KeyFile) // persist the incremented sign counter
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

			// Origin binding v2: hostname joins the signed payload so keys
			// cannot be reused across deployments.
			bindHost := ""
			if u, perr := url.Parse(wsURL); perr == nil {
				bindHost = strings.ToLower(u.Hostname())
			}
			dataToSign := challenge.Nonce + "|" + id.Role + "|" + respPacket.Username + "|" + bindHost
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
		// WebAuthn passkey login (web build only): the page's browser
		// ceremony signs the nonce and fills the packet. A failure here is
		// fatal — the user asked for authenticated login and gets no silent
		// guest fallback.
		if !applyWebPasskey(&respPacket, challenge.Nonce) {
			notifyQuit()
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

	switch authSuccess.Type {
	case "auth_failed":
		fmt.Println("❌ Xác thực bị từ chối:", authSuccess.Error)
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
					fmt.Fprintf(out, "| %s\n", line)
					continue
				}
				if !isShowingJoin && pendingDateBanner != "" {
					fmt.Fprintf(out, "| %s\n", pendingDateBanner)
					pendingDateBanner = ""
				}
				fmt.Fprintf(out, "| %s\n", line)
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

		for range typedLinesCount {
			fmt.Fprint(out, "\033[1A\033[2K\r")
		}

		lines := strings.Split(text, "\n")
		for i, line := range lines {
			line = linkify.Linkify(line)
			if i == 0 {
				fmt.Fprintf(out, "| Bạn: %s\n", line)
			} else {
				fmt.Fprintf(out, "|      %s\n", line)
			}
		}

		if CLI.Tripcode != "" {
			hashTrip := sha256.Sum256([]byte(CLI.Tripcode))
			tripCodeHex := hex.EncodeToString(hashTrip[:])[:8]
			fmt.Fprintf(out, "|  └─ ✍  ◆ %s\n", tripCodeHex)
		}

		err = conn.WriteMessage(wsTextMessage, []byte(text))
		if err != nil {
			fmt.Println("❌ Lỗi gửi tin nhắn:", err)
			break
		}

	}
}

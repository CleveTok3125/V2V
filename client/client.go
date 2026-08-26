package main

import (
	"bufio"
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
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

	Passkey bool   `help:"Đăng nhập bằng WebAuthn passkey (web)" `
	Role    string `help:"Role cần yêu cầu khi dùng passkey"`
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
}

type ClientIdentity struct {
	Role       string `json:"role"`
	PrivateKey string `json:"private_key"`
	HmacShield string `json:"hmac_shield"`
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

// generateKeyInteractive creates a new role identity. Two flavors share the
// same flow, mirroring how roles.json accepts both: a classic ed25519 key
// file, or a software passkey in WebAuthn wire format.
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
	if strings.TrimSpace(kind) == "2" {
		generatePasskeyForRole(reader, role)
		return
	}

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

	clientKey := ClientIdentity{
		Role:       role,
		PrivateKey: hex.EncodeToString(priv),
		HmacShield: hmacShield,
	}
	clientFileData, _ := json.MarshalIndent(clientKey, "", "  ")
	err = os.WriteFile("key.json", clientFileData, 0o600)
	if err != nil {
		fmt.Println("❌ Lỗi lưu file key.json:", err)
		return
	}
	fmt.Println("\nĐã lưu: ./key.json (GIỮ BÍ MẬT FILE NÀY!)")

	serverConfig := map[string]interface{}{
		role: map[string]interface{}{
			"identities": []map[string]string{
				{
					"public_key":  hex.EncodeToString(pub),
					"hmac_shield": hmacShield,
				},
			},
			"can_message_unlimited": unlimited,
			"custom_prefix":         prefix,
		},
	}

	serverFileData, _ := json.MarshalIndent(serverConfig, "", "  ")
	err = os.WriteFile("roles.json", serverFileData, 0o600)
	if err != nil {
		fmt.Println("❌ Lỗi lưu file roles.json:", err)
		return
	}
	fmt.Println("Đã lưu ./roles.json")
}

// generatePasskeyForRole creates a software passkey bound to the given role:
// the private half stays in ./passkey.json on this machine, the public half
// is printed as a roles.json snippet for the admin to import.
func generatePasskeyForRole(reader *bufio.Reader, role string) {
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

	path := "./passkey.json"
	if err := pk.Save(path); err != nil {
		fmt.Println("❌ Không ghi được file:", err)
		return
	}

	snippet, err := pk.RolesSnippet()
	if err != nil {
		fmt.Println("❌ Lỗi dựng cấu hình:", err)
		return
	}
	fmt.Printf("\n💾 Đã lưu khóa bí mật tại %s (chmod 600)\n", path)
	fmt.Println("📤 Gửi đoạn sau cho admin thêm vào roles.json:")
	fmt.Println(snippet)
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
		handled := false
		if pk, perr := LoadPasskeyFile(CLI.KeyFile); perr == nil {
			// Software passkey: sign the handshake nonce natively.
			credID, ad, cd, sig, aerr := pk.BuildAssertion(challenge.Nonce)
			if aerr == nil {
				respPacket.Role = pk.Role
				respPacket.PasskeyID = credID
				respPacket.PasskeyAuthData = ad
				respPacket.PasskeyClientData = cd
				respPacket.PasskeySig = sig
				_ = pk.Save(CLI.KeyFile) // persist the incremented sign counter
				fmt.Printf("🔑 Đang yêu cầu cấp quyền bằng passkey: [%s]...\n", pk.Role)
				handled = true
			} else {
				fmt.Printf("⚠️ Passkey lỗi (%v). Sẽ đăng nhập với quyền khách.\n", aerr)
				handled = true
			}
		}

		if !handled {
			keyData, err := os.ReadFile(CLI.KeyFile)
			if err != nil {
				fmt.Printf("⚠️ Không thể đọc file key (%s). Sẽ đăng nhập với quyền khách.\n", err)
			} else {
				var identity ClientIdentity
				if err := json.Unmarshal(keyData, &identity); err != nil {
					fmt.Println("⚠️ File key sai định dạng JSON. Sẽ đăng nhập với quyền khách.")
				} else if identity.Role != "" && identity.PrivateKey != "" && identity.HmacShield != "" {

					respPacket.Role = identity.Role
					privBytes, err := hex.DecodeString(identity.PrivateKey)

					if err == nil && len(privBytes) == ed25519.PrivateKeySize {
						priv := ed25519.PrivateKey(privBytes)

						dataToSign := challenge.Nonce + "|" + identity.Role + "|" + respPacket.Username
						sig := ed25519.Sign(priv, []byte(dataToSign))
						respPacket.Signature = hex.EncodeToString(sig)

						h := hmac.New(sha512.New, []byte(identity.HmacShield))
						h.Write(sig)
						h.Write([]byte(challenge.Nonce))
						respPacket.Hmac = hex.EncodeToString(h.Sum(nil))

						fmt.Printf("🔑 Đang yêu cầu cấp quyền: [%s]...\n", identity.Role)
					} else {
						fmt.Println("⚠️ Private Key trong file không hợp lệ (Phải là chuỗi Hex 128 ký tự).")
					}
				}
			}
		}
	}

	// WebAuthn passkey login (web build): hand the handshake nonce to the
	// page, let the browser ceremony sign it, and attach the assertion.
	if CLI.Passkey {
		fmt.Printf("🔑 Đang chờ passkey cho role [%s]...\n", CLI.Role)
		respPacket.Role = CLI.Role
		if a, ok := requestAssertion(challenge.Nonce, CLI.Role); ok {
			respPacket.PasskeyID = a.PasskeyID
			respPacket.PasskeyAuthData = a.AuthData
			respPacket.PasskeyClientData = a.ClientData
			respPacket.PasskeySig = a.Sig
			fmt.Println("✅ Passkey đã ký — gửi xác thực...")
		} else {
			fmt.Println("⚠️ Passkey thất bại/hủy — sẽ đăng nhập với quyền khách.")
			respPacket.Role = ""
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
		fmt.Println("⚠️ Cảnh báo lúc đọc Auth (Có thể Server gửi nhầm thứ tự):", err)
	}
	if authSuccess.Type == "auth_success" {
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

		if text == "/help" || text == "/h" {
			fmt.Fprintln(out, "  [Trợ giúp]: Danh sách các lệnh có thể sử dụng:")
			fmt.Fprintln(out, "    - /help, /h      : Hiển thị bảng trợ giúp này")
			fmt.Fprintln(out, "    - /clear, /c     : Xóa sạch màn hình chat")
			fmt.Fprintln(out, "    - /clearhistory, /ch: Xóa file lịch sử gõ phím lưu trên máy")
			fmt.Fprintln(out, "    - /quit, /q      : Rời phòng chat và tắt ứng dụng")
			fmt.Fprintln(out, "    - /showjoin, /sj : Bật/tắt hiện thông báo người khác ra vào phòng cho các tin kế tiếp")
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

package main

import (
	"bufio"
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/alecthomas/kong"
	"github.com/gorilla/websocket"
)

var Version = "dev"

var CLI struct {
	Version   kong.VersionFlag `help:"Hiển thị phiên bản (Git Commit Hash)" short:"v"`
	Server    string           `help:"Link server WebSocket" short:"s"`
	Username  string           `help:"Tên người dùng của bạn" default:"Anonymous" short:"u"`
	UserAgent string           `help:"Tùy chỉnh User-Agent" default:"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36" short:"a"`
	Info      bool             `help:"Kiểm tra thông tin trạng thái của Server" short:"i"`

	KeyFile string `help:"Đường dẫn file chứa khóa xác thực" short:"k"`
	GenKey  bool   `help:"Tạo file key.json và hiển thị cấu hình cho Server" short:"g"`
}

type AuthPacket struct {
	Type      string `json:"type"`
	Nonce     string `json:"nonce,omitempty"`
	Role      string `json:"role,omitempty"`
	Signature string `json:"signature,omitempty"`
	Hmac      string `json:"hmac,omitempty"`
	Username  string `json:"username,omitempty"`
}

type ClientIdentity struct {
	Role       string `json:"role"`
	PrivateKey string `json:"private_key"`
	HmacShield string `json:"hmac_shield"`
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

func generateKeyInteractive() {
	reader := bufio.NewReader(os.Stdin)

	fmt.Print("Nhập tên role (Mặc định: admin): ")
	role, _ := reader.ReadString('\n')
	role = strings.TrimSpace(role)
	if role == "" {
		role = "admin"
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

func main() {
	kong.Parse(&CLI, kong.Vars{
		"version": Version,
	})

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

	headers := http.Header{}
	headers.Add("User-Agent", CLI.UserAgent)

	conn, resp, err := websocket.DefaultDialer.Dial(wsURL, headers)
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
	}

	if CLI.KeyFile != "" {
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

					dataToSign := append([]byte(challenge.Nonce), []byte(identity.Role)...)
					dataToSign = append(dataToSign, []byte(respPacket.Username)...)
					sig := ed25519.Sign(priv, dataToSign)
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

	err = conn.WriteJSON(respPacket)
	if err != nil {
		fmt.Println("❌ Lỗi gửi dữ liệu xác thực:", err)
		return
	}

	var authSuccess AuthPacket
	conn.ReadJSON(&authSuccess)
	if authSuccess.Type == "auth_success" {
		username = authSuccess.Username
	}

	fmt.Println("Đã kết nối với username:", username)
	fmt.Println("Gõ tin nhắn để chat, /help để hiện trợ giúp\n")
	fmt.Print("| > ")

	quitting := make(chan bool, 1)
	var hideJoinLeave bool
	var hideMu sync.RWMutex

	go func() {
		for {
			_, msg, err := conn.ReadMessage()
			if err != nil {
				select {
				case <-quitting:
					return
				default:
					fmt.Print("\r\033[K")
					fmt.Println("\n ❌ Mất kết nối server")
					os.Exit(1)
				}
			}

			text := string(msg)

			hideMu.RLock()
			isHiding := hideJoinLeave
			hideMu.RUnlock()

			if isHiding && strings.Contains(text, "[Hệ thống]:") && (strings.Contains(text, "đã tham gia") || strings.Contains(text, "đã rời")) {
				continue
			}

			paddedText := "| " + strings.ReplaceAll(text, "\n", "\n  ")

			fmt.Print("\r\033[K")
			fmt.Println(paddedText)
			fmt.Print("| > ")
		}
	}()

	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		text := scanner.Text()

		if text == "/quit" || text == "/q" {
			quitting <- true
			conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))

			fmt.Print("\r\033[K")
			fmt.Println("👋 Đang ngắt kết nối... Tạm biệt!")

			time.Sleep(500 * time.Millisecond)
			break
		}

		if text == "/help" || text == "/h" {
			fmt.Print("\r\033[K")
			fmt.Println("  [Trợ giúp]: Danh sách các lệnh có thể sử dụng:")
			fmt.Println("   - /help, /h      : Hiển thị bảng trợ giúp này")
			fmt.Println("   - /quit, /q      : Rời phòng chat và tắt ứng dụng")
			fmt.Println("   - /hideJoin, /hj : Bật/tắt chế độ ẩn thông báo người khác ra vào phòng")
			fmt.Print("| > ")
			continue
		}

		if text == "/hideJoin" || text == "/hj" {
			hideMu.Lock()
			hideJoinLeave = !hideJoinLeave
			status := "ĐÃ ẨN"
			if !hideJoinLeave {
				status = "ĐÃ HIỆN"
			}
			hideMu.Unlock()

			fmt.Print("\r\033[K")
			fmt.Printf("| [Local]: %s thông báo người dùng ra/vào phòng.\n", status)
			fmt.Print("| > ")
			continue
		}

		if strings.TrimSpace(text) != "" {
			text = strings.ReplaceAll(text, "\\n", "\n")

			fmt.Print("\033[1A\r\033[K")

			paddedText := "| Bạn: " + strings.ReplaceAll(text, "\n", "\n|      ")
			fmt.Println(paddedText)

			fmt.Print("| > ")

			err := conn.WriteMessage(websocket.TextMessage, []byte(text))
			if err != nil {
				fmt.Println("❌ Lỗi gửi tin nhắn:", err)
				break
			}
		} else {
			fmt.Print("\r\033[K| > ")
		}
	}
}

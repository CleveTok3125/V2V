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

	"github.com/alecthomas/kong"
	"github.com/chzyer/readline"
	"github.com/gorilla/websocket"
)

var Version = "dev"

var CLI struct {
	Version   kong.VersionFlag `help:"Hiển thị phiên bản (Git Commit Hash)" short:"v"`
	Server    string           `help:"Link server WebSocket" short:"s"`
	Username  string           `help:"Tên người dùng của bạn" default:"Anonymous" short:"u"`
	Tripcode  string           `help:"Mật khẩu bí mật để tạo Chữ ký Tripcode (tùy chọn)" short:"t"`
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
	Tripcode  string `json:"tripcode,omitempty"`
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
		Tripcode: CLI.Tripcode,
		Nonce:    challenge.Nonce,
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

	quitting := make(chan bool, 1)
	var hideJoinLeave bool
	var hideMu sync.RWMutex

	greeting := func(w io.Writer, uname string) {
		fmt.Fprintln(w, "Đã kết nối với username:", uname)
		fmt.Fprintln(w, "Gõ tin nhắn để chat, /help để hiện trợ giúp\n")
	}

	historyFile := filepath.Join(os.TempDir(), "V2V_chat_history.tmp")

	rl, err := readline.NewEx(&readline.Config{
		Prompt:          "| > ",
		HistoryFile:     historyFile,
		InterruptPrompt: "^C",
		EOFPrompt:       "/quit",
	})
	if err != nil {
		fmt.Println("❌ Lỗi khởi tạo readline:", err)
		return
	}
	defer rl.Close()

	go func() {
		for {
			_, msg, err := conn.ReadMessage()
			if err != nil {
				select {
				case <-quitting:
					return
				default:
					fmt.Fprintf(rl.Stdout(), "\r\033[K\n ❌ Mất kết nối server\n")
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

			lines := strings.Split(text, "\n")
			for _, line := range lines {
				fmt.Fprintf(rl.Stdout(), "| %s\n", line)
			}
			rl.Refresh()
		}
	}()

	greeting(rl.Stdout(), username)

	for {
		text, err := rl.Readline()
		if err != nil {
			break
		}

		text = strings.TrimSpace(text)
		if text == "" {
			continue
		}

		if text == "/quit" || text == "/q" {
			quitting <- true
			conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))

			fmt.Fprintf(rl.Stdout(), "👋 Đang ngắt kết nối... Tạm biệt!\n")
			time.Sleep(500 * time.Millisecond)
			break
		}

		if text == "/help" || text == "/h" {
			fmt.Fprintln(rl.Stdout(), "  [Trợ giúp]: Danh sách các lệnh có thể sử dụng:")
			fmt.Fprintln(rl.Stdout(), "    - /help, /h      : Hiển thị bảng trợ giúp này")
			fmt.Fprintln(rl.Stdout(), "    - /clear, /c     : Xóa sạch màn hình chat")
			fmt.Fprintln(rl.Stdout(), "    - /clearhistory, /ch: Xóa file lịch sử gõ phím lưu trên máy")
			fmt.Fprintln(rl.Stdout(), "    - /quit, /q      : Rời phòng chat và tắt ứng dụng")
			fmt.Fprintln(rl.Stdout(), "    - /hideJoin, /hj : Bật/tắt chế độ ẩn thông báo người khác ra vào phòng")
			fmt.Fprintln(rl.Stdout(), "    - Gõ ``` ở đầu và cuối tin nhắn để gửi Code block / nhiều dòng")
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
			fmt.Fprintf(rl.Stdout(), "| [Local]: %s thông báo người dùng ra/vào phòng.\n", status)
			continue
		}

		if text == "/clear" || text == "/c" {
			fmt.Fprint(rl.Stdout(), "\033[H\033[2J")
			greeting(rl.Stdout(), username)
			continue
		}

		if text == "/clearhistory" || text == "/ch" {
			os.Remove(historyFile)
			fmt.Fprintf(rl.Stdout(), "🗑️ Đã xóa file lịch sử gõ phím tại: %s\n", historyFile)
			continue
		}

		typedLinesCount := 1

		if strings.HasPrefix(text, "```") {
			var rawLines []string
			rawLines = append(rawLines, text)

			rl.SetPrompt("| ... ")
			for {
				nextLine, err := rl.Readline()
				if err != nil {
					break
				}
				typedLinesCount++
				rawLines = append(rawLines, nextLine)

				if strings.HasSuffix(strings.TrimSpace(nextLine), "```") {
					break
				}
			}

			rl.SetPrompt("| > ")
			text = strings.Join(rawLines, "\n")
		}

		for range typedLinesCount {
			fmt.Fprint(rl.Stdout(), "\033[1A\033[2K\r")
		}

		lines := strings.Split(text, "\n")
		for i, line := range lines {
			if i == 0 {
				fmt.Fprintf(rl.Stdout(), "| Bạn: %s\n", line)
			} else {
				fmt.Fprintf(rl.Stdout(), "|      %s\n", line)
			}
		}

		if CLI.Tripcode != "" {
			hashTrip := sha256.Sum256([]byte(CLI.Tripcode))
			tripCodeHex := hex.EncodeToString(hashTrip[:])[:8]
			fmt.Fprintf(rl.Stdout(), "|  └─ ✍  ◆ %s\n", tripCodeHex)
		}

		err = conn.WriteMessage(websocket.TextMessage, []byte(text))
		if err != nil {
			fmt.Println("❌ Lỗi gửi tin nhắn:", err)
			break
		}

	}
}

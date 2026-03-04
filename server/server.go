package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"time"
	_ "time/tzdata"
	"unicode/utf8"

	"github.com/gorilla/websocket"
	"github.com/joho/godotenv"
)

func addMessageToHistory(msg string) {
	HistoryMu.Lock()
	defer HistoryMu.Unlock()

	msgSize := len(msg)
	ChatHistory = append(ChatHistory, msg)
	ChatHistorySize += msgSize

	for ChatHistorySize > Cfg.MaxHistoryBytes && len(ChatHistory) > 0 {
		oldestSize := len(ChatHistory[0])
		ChatHistorySize -= oldestSize

		ChatHistory[0] = ""
		ChatHistory = ChatHistory[1:]
	}
}

func broadcast(message string, sender *websocket.Conn) {
	addMessageToHistory(message)

	ClientsMu.Lock()
	defer ClientsMu.Unlock()

	for conn := range Clients {
		if conn != sender {
			if err := conn.WriteMessage(websocket.TextMessage, []byte(message)); err != nil {
				conn.Close()
				delete(Clients, conn)
			}
		}
	}
}

func checkAndBroadcastDate(now time.Time) {
	currentDate := now.Format("02/01/2006")

	LastMessageDateMu.Lock()
	defer LastMessageDateMu.Unlock()

	if LastMessageDate == "" || LastMessageDate != currentDate {
		LastMessageDate = currentDate

		dateMsg := fmt.Sprintf("\x1b[36m--- Ngày %s ---\x1b[0m", currentDate)

		broadcast(dateMsg, nil)
	}
}

// To prevent IP spoofing, only accept IPs sent from Cloudflare
// Change this getClientIP function if you are not using Cloudflare
func getClientIP(r *http.Request) string {
	var ip string

	if cfIP := r.Header.Get("CF-Connecting-IP"); cfIP != "" {
		ip = cfIP
	} else {
		ip, _, _ = net.SplitHostPort(r.RemoteAddr)
	}

	parsedIP := net.ParseIP(strings.TrimSpace(ip))
	if parsedIP == nil {
		fallbackIP, _, _ := net.SplitHostPort(r.RemoteAddr)
		return fallbackIP
	}

	return parsedIP.String()
}

func loadRoles() {
	paths := []string{"./roles.json", "/etc/secrets/roles.json"}
	var data []byte
	var err error

	for _, p := range paths {
		data, err = os.ReadFile(p)
		if err == nil {
			log.Printf("✅ Đã tải cấu hình quyền hạn từ: %s", p)
			break
		}
	}

	if err != nil {
		log.Println("ℹ️ Không tìm thấy roles.json ở bất kỳ thư mục nào (Sẽ hoạt động với quyền User mặc định)")
		return
	}

	if err := json.Unmarshal(data, &RoleRegistry); err != nil {
		log.Fatalf("❌ Lỗi cấu trúc file roles.json: %v", err)
	}
}

func chatHandler(w http.ResponseWriter, r *http.Request) {
	clientIP := getClientIP(r)

	AuthFailsMu.Lock()
	record := AuthFails[clientIP]

	if time.Now().Before(record.UnlockTime) {
		AuthFailsMu.Unlock()
		log.Printf("⛔ [BAN] Từ chối %s. Vui lòng đợi đến %s.", clientIP, record.UnlockTime.Format("15:04:05"))
		http.Error(w, "IP của bạn đang bị khóa tạm thời do xác thực sai nhiều lần.", http.StatusTooManyRequests)
		return
	}
	AuthFailsMu.Unlock()

	LastConnectMu.Lock()
	if lastTime, exists := LastConnectTime[clientIP]; exists {
		if time.Since(lastTime) < Cfg.ConnectionCooldown {
			LastConnectMu.Unlock()
			log.Printf("⛔ Từ chối: %s kết nối ra/vào quá nhanh.\n", clientIP)
			http.Error(w, "Bạn thao tác ra/vào quá nhanh! Vui lòng đợi vài giây rồi thử lại.", http.StatusTooManyRequests)
			return
		}
	}
	LastConnectTime[clientIP] = time.Now()
	LastConnectMu.Unlock()

	IpCountsMu.Lock()
	if IpCounts[clientIP] >= Cfg.MaxConnectionsPerIP {
		IpCountsMu.Unlock()
		log.Printf("⛔ Từ chối: %s đã vượt quá giới hạn %d kết nối.\n", clientIP, Cfg.MaxConnectionsPerIP)
		http.Error(w, "Bạn đã mở quá nhiều kết nối từ địa chỉ IP này.", http.StatusTooManyRequests)
		return
	}
	IpCounts[clientIP]++
	IpCountsMu.Unlock()

	defer func() {
		IpCountsMu.Lock()
		IpCounts[clientIP]--
		if IpCounts[clientIP] <= 0 {
			delete(IpCounts, clientIP)
		}
		IpCountsMu.Unlock()
	}()

	log.Printf("🔌 New request | Client IP: %s | Proxy IP: %s | Upgrade: %s\n", clientIP, r.RemoteAddr, r.Header.Get("Upgrade"))

	conn, err := Upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Println("❌ Upgrade error:", err)
		return
	}
	defer conn.Close()

	perms, authPacket, err := HandleAuth(conn, clientIP)
	if err != nil {
		if authPacket.Role != "" {
			AuthFailsMu.Lock()
			record := AuthFails[clientIP]
			record.FailCount++

			if record.FailCount >= 5 {
				record.UnlockTime = time.Now().Add(5 * time.Minute)
				record.FailCount = 0
			}
			AuthFails[clientIP] = record
			AuthFailsMu.Unlock()

			conn.WriteMessage(websocket.TextMessage, []byte("[Hệ thống]: Xác thực thất bại! Đã ngắt kết nối."))
			return
		}
		return
	} else if authPacket.Role != "" {
		AuthFailsMu.Lock()
		delete(AuthFails, clientIP)
		AuthFailsMu.Unlock()
	}

	username := sanitizeString(authPacket.Username)
	if username == "" {
		username = "Anonymous"
	}

	var displayName string
	if perms.CustomPrefix != "" {
		displayName = perms.CustomPrefix + username
	} else {
		hash := sha256.Sum256([]byte(clientIP))
		ipSuffix := hex.EncodeToString(hash[:])[:4]
		displayName = fmt.Sprintf("%s#%s", username, ipSuffix)
	}

	session := &ClientSession{
		Conn:        conn,
		DisplayName: displayName,
		Perms:       perms,
	}

	ClientsMu.Lock()
	Clients[conn] = session
	ClientsMu.Unlock()

	HistoryMu.RLock()
	historyLen := len(ChatHistory)

	if historyLen > 0 {
		startIndex := 0
		if historyLen > Cfg.MaxHistorySend {
			startIndex = historyLen - Cfg.MaxHistorySend
		}

		conn.WriteMessage(websocket.TextMessage, []byte("--- Lịch sử chat gần đây ---"))

		for i := startIndex; i < historyLen; i++ {
			time.Sleep(5 * time.Millisecond)

			conn.WriteMessage(websocket.TextMessage, []byte(ChatHistory[i]))
		}

		conn.WriteMessage(websocket.TextMessage, []byte("--- Kết thúc lịch sử ---"))
	}
	HistoryMu.RUnlock()

	joinTime := time.Now().In(Cfg.Timezone)
	checkAndBroadcastDate(joinTime)
	joinTimeStr := joinTime.Format("15:04")

	joinMsg := fmt.Sprintf("\x1b[90m%s\x1b[0m [Hệ thống]: %s đã tham gia phòng chat!", joinTimeStr, displayName)
	log.Printf("🟢 [JOIN] %s (IP: %s)\n", displayName, clientIP)
	broadcast(joinMsg, conn)

	lastMessageTime := time.Time{}

	for {
		_, msg, err := conn.ReadMessage()
		if err != nil {
			break
		}

		text := string(msg)
		text = sanitizeString(text)

		if !session.Perms.CanMessageUnlimited {
			if utf8.RuneCountInString(text) > Cfg.MaxMessageLength {
				warning := fmt.Sprintf("[Hệ thống]: Tin nhắn của bạn quá dài (tối đa %d ký tự). Hãy chia nhỏ ra nhé!", Cfg.MaxMessageLength)
				conn.WriteMessage(websocket.TextMessage, []byte(warning))
				continue
			}

			if strings.Count(text, "\n") > Cfg.MaxMessageLine {
				warning := "[Hệ thống]: Tin nhắn chứa quá nhiều dòng. Vui lòng gộp lại để tránh làm trôi khung chat!"
				conn.WriteMessage(websocket.TextMessage, []byte(warning))
				continue
			}

			if time.Since(lastMessageTime) < Cfg.MessageCooldown {
				warning := fmt.Sprintf("[Hệ thống]: Bạn đang chat quá nhanh! Vui lòng đợi %v giữa các tin nhắn.", Cfg.MessageCooldown)
				conn.WriteMessage(websocket.TextMessage, []byte(warning))
				continue
			}
		}

		lastMessageTime = time.Now()

		now := time.Now().In(Cfg.Timezone)
		checkAndBroadcastDate(now)
		timeStr := now.Format("15:04")

		chatMsg := fmt.Sprintf("\x1b[90m%s\x1b[0m %s: %s", timeStr, displayName, text)
		log.Printf("💬 [MSG từ %s]: %s\n", clientIP, chatMsg)
		broadcast(chatMsg, conn)
	}

	ClientsMu.Lock()
	delete(Clients, conn)
	ClientsMu.Unlock()

	leaveTime := time.Now().In(Cfg.Timezone)
	checkAndBroadcastDate(leaveTime)
	leaveTimeStr := leaveTime.Format("15:04")

	leaveMsg := fmt.Sprintf("\x1b[90m%s\x1b[0m [Hệ thống]: %s đã rời phòng chat.", leaveTimeStr, displayName)
	log.Printf("🔴 [LEAVE] %s (IP: %s)\n", displayName, clientIP)
	broadcast(leaveMsg, nil)
}

func main() {
	err := godotenv.Load()
	if err != nil {
		_ = godotenv.Load("/etc/secrets/.env")
	}

	Cfg = AppConfig{
		AllowedOrigins:      strings.Split(os.Getenv("ALLOWED_ORIGINS"), ","),
		MaxConnectionsPerIP: getEnvAsInt("MAX_CONNECTIONS_PER_IP"),
		MaxMessageLength:    getEnvAsInt("MAX_MESSAGE_LENGTH"),
		MaxMessageLine:      getEnvAsInt("MAX_MESSAGE_LINE"),
		MessageCooldown:     getEnvAsDuration("MESSAGE_COOLDOWN"),
		MaxHistoryBytes:     getEnvAsInt("MAX_HISTORY_BYTES"),
		MaxHistorySend:      getEnvAsInt("MAX_HISTORY_SEND"),
		MaxUsernameLength:   getEnvAsInt("MAX_USERNAME_LENGTH"),
		ConnectionCooldown:  getEnvAsDuration("CONNECTION_COOLDOWN"),
		Port:                getSmartEnv("PORT"),
		StatusURL:           getSmartEnv("STATUS_URL"),
		DownloadURL:         getSmartEnv("DOWNLOAD_URL"),
		HomepageURL:         getSmartEnv("HOMEPAGE_URL"),
		InstanceID:          lastAfterDash(getSmartEnv("INSTANCE_ID")),
		Timezone:            getEnvAsLocationOptional("TIMEZONE", "Asia/Ho_Chi_Minh"),
	}

	loadRoles()

	mux := http.NewServeMux()

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if strings.ToLower(r.Header.Get("Upgrade")) == "websocket" {
			chatHandler(w, r)
			return
		}

		uptime := time.Since(ServerStartTime).Round(time.Second)

		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		fmt.Fprintln(w, "WebSocket server is running...\n")
		fmt.Fprintln(w, "Mô tả      : Hệ thống chat ẩn danh")
		fmt.Fprintln(w, "Giao thức  : WebSocket")
		fmt.Fprintf(w, "Instance ID: %s\n", Cfg.InstanceID)
		fmt.Fprintf(w, "Uptime     : %s\n", uptime.String())
		fmt.Fprintf(w, "Múi giờ    : %s\n", Cfg.Timezone)
		fmt.Fprintf(w, "Trạng thái : %s\n", Cfg.StatusURL)
		fmt.Fprintln(w, "------------------------------------")
		fmt.Fprintf(w, "Tải Client : %s\n", Cfg.DownloadURL)
		fmt.Fprintf(w, "Homepage   : %s\n", Cfg.HomepageURL)
	})

	server := &http.Server{
		Addr:              ":" + Cfg.Port,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	log.Println("🚀 Server đang chạy tại port", Cfg.Port)
	log.Fatal(server.ListenAndServe())
}

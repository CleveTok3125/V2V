package main

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"
	_ "time/tzdata"

	"github.com/joho/godotenv"
)

func IsSecuredConnect(w http.ResponseWriter, r *http.Request, clientIP string) bool {
	if !Cfg.Static.RequireTLS {
		log.Printf("⚠️ Server đang không buộc sử dụng kết nối mã hoá")
		return true
	}

	isTLS := r.TLS != nil
	isProxyTLS := strings.ToLower(r.Header.Get("X-Forwarded-Proto")) == "https"
	isLocalhost := clientIP == "127.0.0.1" || clientIP == "::1"

	if isTLS || isProxyTLS || isLocalhost {
		return true
	}

	log.Printf("⚠️ Khóa kết nối không an toàn từ %s (Policy: RequireTLS)", clientIP)
	http.Error(w, "Server bắt buộc sử dụng kết nối mã hóa (wss://).", http.StatusUpgradeRequired)
	return false
}

func (s *ChatServer) ServeWS(w http.ResponseWriter, r *http.Request) {
	clientIP := getClientIP(r)

	if !IsSecuredConnect(w, r, clientIP) {
		return
	}

	if !s.CheckConnectionRate(w, clientIP) {
		return
	}

	if !s.acquireIPConnection(w, clientIP) {
		return
	}

	defer s.releaseIPConnection(clientIP)

	log.Printf("🔌 New request | Client IP: %s | Proxy IP: %s | Upgrade: %s\n", clientIP, r.RemoteAddr, r.Header.Get("Upgrade"))

	conn, err := s.Upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Println("❌ Upgrade error:", err)
		return
	}
	defer conn.Close()

	session, err := s.authenticateClient(conn, clientIP)
	if err != nil {
		return
	}

	s.registerClient(session, clientIP)

	go session.WritePump()

	s.ReadPump(session, clientIP)
}

func (s *ChatServer) StartCleanupTasks() {
	ticker := time.NewTicker(10 * time.Minute)

	go func() {
		for range ticker.C {
			now := time.Now()

			s.AuthFailsMu.Lock()
			for ip, record := range s.AuthFails {
				if now.After(record.UnlockTime) {
					delete(s.AuthFails, ip)
				}
			}
			s.AuthFailsMu.Unlock()

			s.LastConnectMu.Lock()
			for ip, lastTime := range s.LastConnectTime {
				if time.Since(lastTime) > Cfg.Dynamic.Load().ConnectionCooldown {
					delete(s.LastConnectTime, ip)
				}
			}
			s.LastConnectMu.Unlock()
		}
	}()
}

func loadStaticConfig() StaticConfig {
	rawInstanceID := getEnvOptional("INSTANCE_ID", "AUTO")
	var instanceID string
	if rawInstanceID == "AUTO" {
		instanceID = generateRandomID(6)
	} else {
		instanceID = lastAfterDash(getSmartEnv("INSTANCE_ID"))
	}

	return StaticConfig{
		AllowedOrigins: strings.Split(os.Getenv("ALLOWED_ORIGINS"), ","),
		RequireTLS:     getEnvAsBoolOptional("REQUIRE_TLS", false),
		Port:           getSmartEnv("PORT"),
		InstanceID:     instanceID,
		Timezone:       getEnvAsLocationOptional("TIMEZONE", "Asia/Ho_Chi_Minh"),
		LogFilePath:    getSmartEnv("LOG_FILE_PATH"),
		MaxLogSizeMB:   getEnvAsInt("MAX_LOG_SIZE_MB"),
	}
}

func loadDynamicConfig() DynamicConfig {
	return DynamicConfig{
		StatusURL:   getSmartEnv("STATUS_URL"),
		DownloadURL: getSmartEnv("DOWNLOAD_URL"),
		HomepageURL: getSmartEnv("HOMEPAGE_URL"),

		MaxConnectionsPerIP: getEnvAsInt("MAX_CONNECTIONS_PER_IP"),
		MaxMessageLength:    getEnvAsInt("MAX_MESSAGE_LENGTH"),
		MaxMessageLine:      getEnvAsInt("MAX_MESSAGE_LINE"),
		MessageCooldown:     getEnvAsDuration("MESSAGE_COOLDOWN"),
		MaxHistoryBytes:     getEnvAsInt("MAX_HISTORY_BYTES"),
		MaxHistorySend:      getEnvAsInt("MAX_HISTORY_SEND"),
		MaxUsernameLength:   getEnvAsInt("MAX_USERNAME_LENGTH"),
		MaxTripcodeLength:   getEnvAsIntOptional("MAX_TRIPCODE_LENGTH", 64),
		ConnectionCooldown:  getEnvAsDuration("CONNECTION_COOLDOWN"),
	}
}

func ReloadDynamicConfig() {
	_ = godotenv.Overload(".env")
	if _, err := os.Stat("/etc/secrets/.env"); err == nil {
		_ = godotenv.Overload("/etc/secrets/.env")
	}

	newDynamic := loadDynamicConfig()

	Cfg.Dynamic.Store(&newDynamic)
	log.Println("🔄 [HOT-RELOAD] Đã cập nhật thành công các thông số logic!")
}

func (s *ChatServer) WatchEnvFile() {
	var lastModTime time.Time
	ticker := time.NewTicker(10 * time.Second)

	go func() {
		for range ticker.C {
			paths := []string{".env", "/etc/secrets/.env"}
			for _, p := range paths {
				info, err := os.Stat(p)
				if err == nil {
					if lastModTime.IsZero() {
						lastModTime = info.ModTime()
						break
					}
					if info.ModTime().After(lastModTime) {
						lastModTime = info.ModTime()

						ReloadDynamicConfig()
						log.Printf("⚠️ Lưu ý: File %s vừa đổi. Nếu bạn sửa Static Config, vui lòng RESTART server!", p)
					}
					break
				}
			}
		}
	}()
}

func main() {
	err := godotenv.Load()
	if err != nil {
		_ = godotenv.Load("/etc/secrets/.env")
	}

	Cfg.Static = loadStaticConfig()

	initialDynamic := loadDynamicConfig()
	Cfg.Dynamic.Store(&initialDynamic)

	chatApp := NewChatServer()

	chatApp.LoadRoles()
	chatApp.WatchEnvFile()
	chatApp.StartCleanupTasks()

	mux := http.NewServeMux()

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if strings.ToLower(r.Header.Get("Upgrade")) == "websocket" {
			chatApp.ServeWS(w, r)
			return
		}

		dynCfg := Cfg.Dynamic.Load()

		uptime := time.Since(chatApp.StartTime).Round(time.Second)

		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		fmt.Fprintln(w, "WebSocket server is running...\n")
		fmt.Fprintln(w, "Mô tả      : Hệ thống chat ẩn danh")
		fmt.Fprintln(w, "Giao thức  : WebSocket")
		fmt.Fprintf(w, "Instance ID: %s\n", Cfg.Static.InstanceID)
		fmt.Fprintf(w, "Uptime     : %s\n", uptime.String())
		fmt.Fprintf(w, "Múi giờ    : %s\n", Cfg.Static.Timezone)
		fmt.Fprintf(w, "Trạng thái : %s\n", dynCfg.StatusURL)
		fmt.Fprintln(w, "------------------------------------")
		fmt.Fprintf(w, "Tải Client : %s\n", dynCfg.DownloadURL)
		fmt.Fprintf(w, "Homepage   : %s\n", dynCfg.HomepageURL)
	})

	InitLogger(Cfg.Static.LogFilePath, Cfg.Static.MaxLogSizeMB)

	server := &http.Server{
		Addr:              ":" + Cfg.Static.Port,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	log.Println("🚀 Server đang chạy tại port", Cfg.Static.Port)
	log.Fatal(server.ListenAndServe())
}

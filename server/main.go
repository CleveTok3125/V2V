package main

import (
	"fmt"
	"log"
	"mime"
	"net/http"
	"os"
	"path"
	"path/filepath"
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

// cacheControl sets caching headers for /web/ assets. Versioned assets that
// actually exist get long-lived caching; index.html stays fresh so a new
// build is picked up immediately; missing files get a direct 404 with
// no-store so a CDN can never cache a stale 404 as immutable (Go's net/http
// strips Cache-Control set on ServeFile error responses).
func cacheControl(next http.Handler, root string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ext := strings.ToLower(path.Ext(r.URL.Path))
		rel := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")

		if info, err := os.Stat(filepath.Join(root, rel)); err == nil {
			if !info.IsDir() && rel != "version.js" &&
				(ext == ".wasm" || ext == ".js" || ext == ".css") {
				w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			} else if info.IsDir() || rel == "index.html" {
				w.Header().Set("Cache-Control", "no-cache")
			}
			next.ServeHTTP(w, r)
			return
		}

		w.Header().Set("Cache-Control", "no-store")
		http.NotFound(w, r)
	})
}

func (s *ChatServer) StartCleanupTasks() {
	ticker := time.NewTicker(10 * time.Minute)

	go func() {
		for range ticker.C {
			now := time.Now()

			s.AuthFailsMu.Lock()
			for ip, record := range s.AuthFails {
				// Only purge records that actually reached a ban and have
				// expired; in-progress counters (zero UnlockTime) must survive
				// so a slow brute-force cannot reset them every tick.
				if !record.UnlockTime.IsZero() && now.After(record.UnlockTime) {
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

func loadStaticConfig() (StaticConfig, error) {
	loader := &envLoader{}
	rawInstanceID := getEnvOptional("INSTANCE_ID", "AUTO")
	var instanceID string
	if rawInstanceID == "AUTO" {
		instanceID = generateRandomID(6)
	} else {
		instanceID = lastAfterDash(loader.Smart("INSTANCE_ID"))
	}

	cfg := StaticConfig{
		AllowedOrigins:       strings.Split(os.Getenv("ALLOWED_ORIGINS"), ","),
		RequireTLS:           getEnvAsBoolOptional("REQUIRE_TLS", false),
		Port:                 loader.Smart("PORT"),
		InstanceID:           instanceID,
		Timezone:             getEnvAsLocationOptional("TIMEZONE", "Asia/Ho_Chi_Minh"),
		LogFilePath:          loader.Smart("LOG_FILE_PATH"),
		MaxLogSizeMB:         loader.Int("MAX_LOG_SIZE_MB"),
		HistoryFilePath:      loader.Smart("HISTORY_FILE_PATH"),
		MaxHistoryFileSizeMB: loader.Int("MAX_HISTORY_FILE_SIZE_MB"),
	}
	if err := loader.Err(); err != nil {
		return StaticConfig{}, err
	}

	return cfg, nil
}

func loadDynamicConfig() (DynamicConfig, error) {
	loader := &envLoader{}

	cfg := DynamicConfig{
		StatusURL:           loader.Smart("STATUS_URL"),
		DownloadURL:         loader.Smart("DOWNLOAD_URL"),
		HomepageURL:         loader.Smart("HOMEPAGE_URL"),
		MaxConnectionsPerIP: loader.Int("MAX_CONNECTIONS_PER_IP"),
		MaxMessageLength:    loader.Int("MAX_MESSAGE_LENGTH"),
		MaxMessageLine:      loader.Int("MAX_MESSAGE_LINE"),
		MessageCooldown:     loader.Duration("MESSAGE_COOLDOWN"),
		IdleChatTimeout:     loader.Duration("IDLE_CHAT_TIMEOUT"),
		MaxHistoryBytes:     loader.Int("MAX_HISTORY_BYTES"),
		MaxHistorySend:      loader.Int("MAX_HISTORY_SEND"),
		MaxUsernameLength:   loader.Int("MAX_USERNAME_LENGTH"),
		MaxTripcodeLength:   getEnvAsIntOptional("MAX_TRIPCODE_LENGTH", 64),
		ConnectionCooldown:  loader.Duration("CONNECTION_COOLDOWN"),
	}
	if err := loader.Err(); err != nil {
		return DynamicConfig{}, err
	}

	return cfg, nil
}

func ReloadDynamicConfig() {
	for _, p := range EnvFilePaths {
		if _, err := os.Stat(p); err == nil {
			_ = godotenv.Overload(p)
			break
		}
	}

	newDynamic, err := loadDynamicConfig()
	if err != nil {
		log.Printf("❌ [HOT-RELOAD] Không thể nạp lại dynamic config: %v", err)
		return
	}

	Cfg.Dynamic.Store(&newDynamic)
	log.Println("🔄 [HOT-RELOAD] Đã cập nhật thành công các thông số logic!")
}

func (s *ChatServer) WatchEnvFile() {
	var lastModTime time.Time
	ticker := time.NewTicker(10 * time.Second)

	go func() {
		for range ticker.C {
			for _, p := range EnvFilePaths {
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

func (s *ChatServer) WatchRolesFile() {
	var lastModTime time.Time
	ticker := time.NewTicker(10 * time.Second)

	go func() {
		for range ticker.C {
			for _, p := range RolesFilePaths {
				info, err := os.Stat(p)
				if err == nil {
					if lastModTime.IsZero() {
						lastModTime = info.ModTime()
						break
					}

					if info.ModTime().After(lastModTime) {
						lastModTime = info.ModTime()
						log.Printf("🔄 [HOT-RELOAD] Phát hiện thay đổi trong %s, đang nạp lại roles...", p)

						s.LoadRoles()
					}
					break
				}
			}
		}
	}()
}

func main() {
	for _, p := range EnvFilePaths {
		if err := godotenv.Load(p); err == nil {
			log.Printf("✅ Đã nạp cấu hình môi trường từ: %s", p)
			break
		}
	}

	staticCfg, err := loadStaticConfig()
	if err != nil {
		log.Fatalf("❌ CRITICAL ERROR: %v", err)
	}
	Cfg.Static = staticCfg

	initialDynamic, err := loadDynamicConfig()
	if err != nil {
		log.Fatalf("❌ CRITICAL ERROR: %v", err)
	}
	Cfg.Dynamic.Store(&initialDynamic)

	if err := InitLogger(Cfg.Static.LogFilePath, Cfg.Static.MaxLogSizeMB); err != nil {
		log.Fatalf("❌ CRITICAL ERROR: %v", err)
	}

	chatApp := NewChatServer()
	if err := chatApp.InitHistoryStore(Cfg.Static.HistoryFilePath, Cfg.Static.MaxHistoryFileSizeMB); err != nil {
		log.Fatalf("❌ CRITICAL ERROR: %v", err)
	}

	chatApp.LoadRoles()
	chatApp.WatchEnvFile()
	chatApp.WatchRolesFile()
	chatApp.StartCleanupTasks()

	mux := http.NewServeMux()

	mime.AddExtensionType(".wasm", "application/wasm")
	mux.Handle("/web/", http.StripPrefix("/web/", cacheControl(http.FileServer(http.Dir("webterm")), "webterm")))

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

	server := &http.Server{
		Addr:              ":" + Cfg.Static.Port,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	log.Println("🚀 Server đang chạy tại port", Cfg.Static.Port)
	log.Fatal(server.ListenAndServe())
}

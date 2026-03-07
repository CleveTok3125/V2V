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

func (s *ChatServer) ServeWS(w http.ResponseWriter, r *http.Request) {
	clientIP := getClientIP(r)

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
				if time.Since(lastTime) > Cfg.ConnectionCooldown {
					delete(s.LastConnectTime, ip)
				}
			}
			s.LastConnectMu.Unlock()
		}
	}()
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

	chatApp := NewChatServer()

	chatApp.LoadRoles()
	chatApp.StartCleanupTasks()

	mux := http.NewServeMux()

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if strings.ToLower(r.Header.Get("Upgrade")) == "websocket" {
			chatApp.ServeWS(w, r)
			return
		}

		uptime := time.Since(chatApp.StartTime).Round(time.Second)

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

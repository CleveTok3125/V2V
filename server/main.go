package main

import (
	"encoding/json"
	"fmt"
	"log"
	"mime"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	_ "time/tzdata"

	"github.com/CleveTok3125/V2V/internal/env"
	"github.com/CleveTok3125/V2V/internal/guard"
	"github.com/CleveTok3125/V2V/internal/trustedproxy"
	"github.com/joho/godotenv"
)

// onionRequest reports whether this request targets a configured onion host.
func onionRequest(r *http.Request) bool {
	return Cfg.Static.OnionEnabled() && Cfg.Static.IsOnionHost(r.Host)
}

// webAllowed reports whether the WASM web client may be served. WebEnabled
// is the master switch; onion requests are then denied unless the operator
// opts in with ONION_ALLOW_WEB.
func webAllowed(r *http.Request) bool {
	if !Cfg.Static.WebEnabled {
		return false
	}
	return !onionRequest(r) || Cfg.Static.Onion.AllowWeb
}

// passkeyAllowed reports whether the passkey ceremony may run. Onion
// requests are denied unless the operator opts in; the master WebAuthn
// switch is enforced separately where ceremony/assertion happens.
func passkeyAllowed(r *http.Request) bool {
	return !onionRequest(r) || Cfg.Static.Onion.AllowPasskey
}

// passkeyDisabledForOnion reports whether the onion restriction alone blocks
// passkey auth. Master WebAuthn enablement is enforced separately, so this
// can only restrict, never enable.
func passkeyDisabledForOnion(onion bool) bool {
	return onion && !Cfg.Static.Onion.AllowPasskey
}

func IsSecuredConnect(w http.ResponseWriter, r *http.Request, outcome trustedproxy.Outcome) bool {
	if !Cfg.Static.RequireTLS {
		logWarnf("⚠️ Server đang không buộc sử dụng kết nối mã hoá")
		return true
	}

	clientIP := outcome.ClientIP
	isTLS := r.TLS != nil
	// X-Forwarded-Proto is only meaningful behind a trusted proxy:
	// anyone can send that header directly.
	isProxyTLS := outcome.Trusted && strings.ToLower(r.Header.Get("X-Forwarded-Proto")) == "https"
	isLocalhost := clientIP == "127.0.0.1" || clientIP == "::1"
	// Onion ingress: Tor provides end-to-end encryption and authenticates
	// the .onion address, so TLS would be redundant (and no CA issues certs
	// for .onion). Only accept this from a loopback or onion-trusted hop,
	// never via a header-trusting proxy whose client IP is attacker-controlled.
	isOnion := onionRequest(r) && !outcome.Trusted &&
		(isLocalhost || trustedproxy.ContainsIP(Cfg.Static.Onion.Trust, outcome.RemoteIP))

	if isTLS || isProxyTLS || isLocalhost || isOnion {
		if isOnion {
			logInfof("🧅 Onion ingress accepted for %s (Tor transport encryption)", trustedproxy.Clip(r.Host, 200))
		}
		return true
	}

	logWarnf("⚠️ Khóa kết nối không an toàn từ %s (Policy: RequireTLS)", clientIP)
	http.Error(w, "Server bắt buộc sử dụng kết nối mã hóa (wss://).", http.StatusUpgradeRequired)
	return false
}

func (s *ChatServer) ServeWS(w http.ResponseWriter, r *http.Request) {
	outcome := resolveClientIP(r)
	if outcome.Reject {
		logWarnf("⛔ [PROXY] Reject %s (%s): %s", trustedproxy.Clip(outcome.RemoteIP, 200), outcome.Reason, proxyHeadersForLog(r))
		http.Error(w, "Untrusted proxy.", http.StatusForbidden)
		return
	}
	clientIP := outcome.ClientIP
	if outcome.Provider == "none" || outcome.Provider == "direct" {
		logWarnf("⚠️ [PROXY] Direct connection from %s via %s (no proxy headers trusted)", trustedproxy.Clip(outcome.RemoteIP, 200), outcome.Provider)
	}

	if !IsSecuredConnect(w, r, outcome) {
		return
	}

	if !s.CheckConnectionRate(w, clientIP) {
		return
	}

	if !s.acquireIPConnection(w, clientIP) {
		return
	}

	defer s.releaseIPConnection(clientIP)

	logInfof("🔌 New request | Client IP: %s | Proxy IP: %s | Via: %s trusted=%v | Upgrade: %s | %s\n", clientIP, r.RemoteAddr, outcome.Provider, outcome.Trusted, r.Header.Get("Upgrade"), proxyHeadersForLog(r))

	conn, err := s.Upgrader.Upgrade(w, r, nil)
	if err != nil {
		logErrorf("❌ Upgrade error: %v", err)
		return
	}
	defer conn.Close()

	session, err := s.authenticateClient(conn, clientIP, clientHost(r), onionRequest(r))
	if err != nil {
		return
	}

	s.serveAuthenticated(session, clientIP)
}

// serveAuthenticated runs the pumps around registration. WritePump must
// start BEFORE registerClient: SendChatHistory pushes up to
// MaxHistorySend lines into the buffered Send channel synchronously, so
// registering first with a full history and no reader deadlocks the
// handshake forever.
func (s *ChatServer) serveAuthenticated(session *ClientSession, clientIP string) {
	go session.WritePump()

	s.Hub.registerClient(session, clientIP)

	s.ReadPump(session, clientIP)
}

func (s *ChatServer) StartCleanupTasks() {
	ticker := time.NewTicker(10 * time.Minute)

	go func() {
		for range ticker.C {
			now := time.Now()

			s.AuthFailsMu.Lock()
			for ip, record := range s.AuthFails {
				// Purge expired bans and idle in-progress counters. The
				// idle TTL is far longer than the ban window, so a slow
				// brute-force cannot reset its count between ticks.
				if guard.ShouldPrune(record, now, guard.AuthFailTTL) {
					delete(s.AuthFails, ip)
				}
			}
			s.AuthFailsMu.Unlock()

			s.pruneTripChains(now)

			s.LastConnectMu.Lock()
			for ip, lastTime := range s.LastConnectTime {
				if time.Since(lastTime) > Cfg.Dynamic.Load().ConnectionCooldown {
					delete(s.LastConnectTime, ip)
				}
			}
			s.LastConnectMu.Unlock()
		}
	}()

	// Sweep expired pre-auth nonces on a slow ticker instead of one
	// timer per connection.
	go func() {
		nonceSweep := time.NewTicker(30 * time.Second)
		defer nonceSweep.Stop()
		for range nonceSweep.C {
			now := time.Now()
			s.ActiveNonces.Range(func(k, v any) bool {
				if meta, ok := v.(NonceMeta); ok && now.After(meta.ExpiresAt) {
					s.ActiveNonces.Delete(k)
				}
				return true
			})
		}
	}()
}

func loadStaticConfig() (StaticConfig, error) {
	loader := &envLoader{}
	rawInstanceID := getEnvFallback("INSTANCE_ID", "AUTO")
	var instanceID string
	if rawInstanceID == "AUTO" {
		instanceID = generateRandomID(6)
	} else {
		instanceID = lastAfterDash(loader.Smart("INSTANCE_ID"))
	}

	onionHosts, err := parseOnionHosts(getEnvFallback("ONION_HOSTS", ""))
	if err != nil {
		return StaticConfig{}, err
	}

	noContentLogs := getEnvAsBoolFallback("NO_CONTENT_LOGS", false)
	logFilePath := dataPath("app.log")
	if raw := os.Getenv("LOG_FILE_PATH"); strings.TrimSpace(raw) != "" {
		logFilePath = resolveUnderRoot(ServerRoot, raw)
	}
	historyFilePath := dataPath("history.jsonl")
	if raw := os.Getenv("HISTORY_FILE_PATH"); strings.TrimSpace(raw) != "" {
		historyFilePath = resolveUnderRoot(ServerRoot, raw)
	}
	if noContentLogs {
		if strings.TrimSpace(os.Getenv("LOG_FILE_PATH")) != "" || strings.TrimSpace(os.Getenv("HISTORY_FILE_PATH")) != "" {
			logWarnf("⚠️ NO_CONTENT_LOGS=true: LOG_FILE_PATH/HISTORY_FILE_PATH bị bỏ qua (RAM-only)")
		}
		warnStaleContentFiles(logFilePath, historyFilePath)
	}
	logFilePath, historyFilePath = effectiveStoragePaths(noContentLogs, logFilePath, historyFilePath)

	trustedProxyDir := filepath.Join(ServerRoot, DefaultTrustedProxyDir)
	if raw := os.Getenv(env.KeyTrustedProxyDir); strings.TrimSpace(raw) != "" {
		trustedProxyDir = resolveUnderRoot(ServerRoot, raw)
	}

	cfg := StaticConfig{
		AllowedOrigins:       strings.Split(env.AllowedOrigins(), ","),
		RequireTLS:           getEnvAsBoolFallback("REQUIRE_TLS", true),
		Port:                 loader.Smart("PORT"),
		InstanceID:           instanceID,
		Timezone:             getEnvAsLocationFallback("TIMEZONE", "Asia/Ho_Chi_Minh"),
		LogFilePath:          logFilePath,
		MaxLogSizeMB:         loader.Int("MAX_LOG_SIZE_MB"),
		HistoryFilePath:      historyFilePath,
		MaxHistoryFileSizeMB: loader.Int("MAX_HISTORY_FILE_SIZE_MB"),
		NoContentLogs:        noContentLogs,
		Root:                 ServerRoot,
		TrustedProxyDir:      trustedProxyDir,
		WebEnabled:           getEnvAsBoolFallback("WEB_ENABLED", true),
		Onion: OnionConfig{
			Hosts:        onionHosts,
			AllowWeb:     getEnvAsBoolFallback("ONION_ALLOW_WEB", false),
			AllowPasskey: getEnvAsBoolFallback("ONION_ALLOW_PASSKEY", false),
		},
	}
	if err := loader.Err(); err != nil {
		return StaticConfig{}, err
	}
	// PROXY_PROVIDER is required with no implicit default: the
	// operator must state the proxy chain explicitly (fail-closed).
	chain, err := trustedproxy.ParseChain(loader.Smart(env.KeyProxyProvider))
	if err != nil {
		return StaticConfig{}, err
	}
	cfg.ProxyChain = chain

	return cfg, nil
}

// effectiveStoragePaths applies the NO_CONTENT_LOGS policy: content paths
// are cleared so the logger and history store stay off-disk.
func effectiveStoragePaths(noContentLogs bool, logPath, historyPath string) (string, string) {
	if noContentLogs {
		return "", ""
	}
	return logPath, historyPath
}

// warnStaleContentFiles warns about pre-existing history/log artifacts when
// NO_CONTENT_LOGS starts. It never deletes: removal stays an operator action.
func warnStaleContentFiles(logPath, historyPath string) {
	for _, p := range []string{
		historyPath, historyPath + ".old", historyPath + ".old.zst",
		logPath, logPath + ".old",
	} {
		if p == "" {
			continue
		}
		if _, err := os.Stat(p); err == nil {
			logWarnf("⚠️ NO_CONTENT_LOGS: còn file %s trên đĩa; hãy tự xoá nếu muốn sạch dấu vết", p)
		}
	}
}

func loadDynamicConfig() (DynamicConfig, error) {
	loader := &envLoader{}

	cfg := DynamicConfig{
		StatusURL:              loader.Optional("STATUS_URL"),
		DownloadURL:            loader.Optional("DOWNLOAD_URL"),
		HomepageURL:            loader.Optional("HOMEPAGE_URL"),
		MaxConnectionsPerIP:    loader.Int("MAX_CONNECTIONS_PER_IP"),
		MaxMessageLength:       loader.Int("MAX_MESSAGE_LENGTH"),
		MaxMessageLine:         loader.Int("MAX_MESSAGE_LINE"),
		MessageCooldown:        loader.Duration("MESSAGE_COOLDOWN"),
		IdleChatTimeout:        loader.Duration("IDLE_CHAT_TIMEOUT"),
		MaxHistoryBytes:        loader.Int("MAX_HISTORY_BYTES"),
		MaxHistorySend:         loader.Int("MAX_HISTORY_SEND"),
		HistorySegmentCooldown: loader.Duration("HISTORY_SEGMENT_COOLDOWN"),
		HistoryDiskLookup:      loader.Int("HISTORY_DISK_LOOKUP"),
		MaxUsernameLength:      loader.Int("MAX_USERNAME_LENGTH"),
		MaxTripcodeLength:      getEnvAsIntFallback("MAX_TRIPCODE_LENGTH", 64),
		ConnectionCooldown:     loader.Duration("CONNECTION_COOLDOWN"),
	}
	if err := loader.Err(); err != nil {
		return DynamicConfig{}, err
	}
	// Fail-safe floor: a zero/negative replay window would silently send
	// empty history on every connect. Mirror the client backfill default.
	if cfg.MaxHistorySend <= 0 {
		cfg.MaxHistorySend = 500
	}
	// Fail-closed floors: a missing/zero segment throttle would let one
	// client re-scan history unthrottled; an out-of-range disk tier must
	// never widen reads beyond what the operator picked.
	if cfg.HistorySegmentCooldown <= 0 {
		cfg.HistorySegmentCooldown = 2 * time.Second
	}
	if cfg.HistoryDiskLookup < DiskLookupOff || cfg.HistoryDiskLookup > DiskLookupArchive {
		cfg.HistoryDiskLookup = DiskLookupOff
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
		logErrorf("❌ [HOT-RELOAD] Không thể nạp lại dynamic config: %v", err)
		return
	}

	Cfg.Dynamic.Store(&newDynamic)
	logInfo("🔄 [HOT-RELOAD] Đã cập nhật thành công các thông số logic!")
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
						logWarnf("⚠️ Lưu ý: File %s vừa đổi. Nếu bạn sửa Static Config, vui lòng RESTART server!", p)
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
						logInfof("🔄 [HOT-RELOAD] Phát hiện thay đổi trong %s, đang nạp lại roles...", p)

						// Runtime failures keep the old registry: only
						// the boot path below is fatal.
						if err := s.LoadRoles(); err != nil {
							logErrorf("❌ [HOT-RELOAD LỖI] %v. Đang giữ nguyên Roles cũ!", err)
						}
					}
					break
				}
			}
		}
	}()
}

func main() {
	// The instance root must be known before the .env load, since it is
	// what locates that .env: V2V_ROOT (default instances/default).
	InitServerPaths(DefaultServerRoot())

	for _, p := range EnvFilePaths {
		if err := godotenv.Load(p); err == nil {
			logInfof("✅ Đã nạp cấu hình môi trường từ: %s", p)
			break
		}
	}

	staticCfg, err := loadStaticConfig()
	if err != nil {
		log.Fatalf("❌ CRITICAL ERROR: %v", err)
	}
	Cfg.Static = staticCfg

	proxyChain, err := initProxyChain(Cfg.Static.TrustedProxyDir, Cfg.Static.ProxyChain)
	if err != nil {
		log.Fatalf("❌ CRITICAL ERROR: %v", err)
	}
	ProxyChain = proxyChain

	if Cfg.Static.OnionEnabled() {
		onionTrust, err := initOnionTrust(Cfg.Static.TrustedProxyDir)
		if err != nil {
			log.Fatalf("❌ CRITICAL ERROR: onion trust: %v", err)
		}
		Cfg.Static.Onion.Trust = onionTrust
	}

	initialDynamic, err := loadDynamicConfig()
	if err != nil {
		log.Fatalf("❌ CRITICAL ERROR: %v", err)
	}
	Cfg.Dynamic.Store(&initialDynamic)

	if err := InitLogger(Cfg.Static.LogFilePath, Cfg.Static.MaxLogSizeMB); err != nil {
		log.Fatalf("❌ CRITICAL ERROR: %v", err)
	}

	chatApp := NewChatServer()
	sid, err := LoadOrCreateServerIdentity(dataPath("server_identity.json"))
	if err != nil {
		log.Fatalf("❌ CRITICAL ERROR: cannot load server identity: %v", err)
	}
	chatApp.ServerID = sid
	if err := chatApp.InitHistoryStore(Cfg.Static.HistoryFilePath, Cfg.Static.MaxHistoryFileSizeMB); err != nil {
		log.Fatalf("❌ CRITICAL ERROR: %v", err)
	}

	// Booting without roles would silently grant default permissions
	// to everyone: fail closed instead.
	if err := chatApp.LoadRoles(); err != nil {
		log.Fatalf("❌ CRITICAL ERROR: %v", err)
	}
	chatApp.WatchEnvFile()
	chatApp.WatchRolesFile()
	chatApp.StartCleanupTasks()

	mux := http.NewServeMux()

	mime.AddExtensionType(".wasm", "application/wasm")
	webtermDir = resolveWebtermDir(executableDir())
	webHandler := http.StripPrefix("/web/", webFilesHandler(webtermDir))
	mux.HandleFunc("/web/", func(w http.ResponseWriter, r *http.Request) {
		if !webAllowed(r) {
			if !Cfg.Static.WebEnabled {
				logWarnf("⛔ [WEB] Chặn web client từ %s (WEB_ENABLED=false)", trustedproxy.Clip(r.Host, 200))
			} else {
				logWarnf("⛔ [ONION] Chặn web client từ %s (ONION_ALLOW_WEB=false)", trustedproxy.Clip(r.Host, 200))
			}
			http.NotFound(w, r)
			return
		}
		webHandler.ServeHTTP(w, r)
	})

	LoadWebauthnEnv()
	mux.HandleFunc("/webauthn/enroll/begin", func(w http.ResponseWriter, r *http.Request) {
		if !passkeyAllowed(r) {
			logWarnf("⛔ [ONION] Chặn enroll từ %s (ONION_ALLOW_PASSKEY=false)", trustedproxy.Clip(r.Host, 200))
			http.NotFound(w, r)
			return
		}
		if rejectUntrustedProxy(w, r) {
			return
		}
		chatApp.handleEnrollBegin(w, r)
	})
	mux.HandleFunc("/webauthn/enroll/finish", func(w http.ResponseWriter, r *http.Request) {
		if !passkeyAllowed(r) {
			logWarnf("⛔ [ONION] Chặn enroll từ %s (ONION_ALLOW_PASSKEY=false)", trustedproxy.Clip(r.Host, 200))
			http.NotFound(w, r)
			return
		}
		if rejectUntrustedProxy(w, r) {
			return
		}
		chatApp.handleEnrollFinish(w, r)
	})

	mux.HandleFunc("/api/server_pubkey", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		pub := ""
		if chatApp.ServerID != nil {
			pub = chatApp.ServerID.PublicKey
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"public_key": pub})
	})

	mux.HandleFunc("/api/trip/verify", func(w http.ResponseWriter, r *http.Request) {
		if rejectUntrustedProxy(w, r) {
			return
		}
		chatApp.handleTripVerify(w, r)
	})
	mux.HandleFunc("/api/version", handleAPIVersion)

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if strings.ToLower(r.Header.Get("Upgrade")) == "websocket" {
			chatApp.ServeWS(w, r)
			return
		}

		dynCfg := Cfg.Dynamic.Load()

		uptime := time.Since(chatApp.StartTime).Round(time.Second)

		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "WebSocket server is running...\n\n")
		fmt.Fprintln(w, "Mô tả      : Hệ thống chat ẩn danh")
		fmt.Fprintln(w, "Giao thức  : WebSocket")
		fmt.Fprintf(w, "Phiên bản  : %s\n", Version)
		fmt.Fprintf(w, "Instance ID: %s\n", Cfg.Static.InstanceID)
		fmt.Fprintf(w, "Uptime     : %s\n", uptime.String())
		fmt.Fprintf(w, "Múi giờ    : %s\n", Cfg.Static.Timezone)
		if dynCfg.StatusURL != "" {
			fmt.Fprintf(w, "Trạng thái : %s\n", dynCfg.StatusURL)
		}
		fmt.Fprintln(w, "------------------------------------")
		fmt.Fprintf(w, "Blog       : /blog\n")
		fmt.Fprintf(w, "Web Client : /web\n")
		if dynCfg.DownloadURL != "" {
			fmt.Fprintf(w, "Tải Client : %s\n", dynCfg.DownloadURL)
		}
		if dynCfg.HomepageURL != "" {
			fmt.Fprintf(w, "Homepage   : %s\n", dynCfg.HomepageURL)
		}
		fmt.Fprintln(w, "------------------------------------")
		if chatApp.ServerID != nil && chatApp.ServerID.PublicKey != "" {
			fmt.Fprintf(w, "Server Pubkey: %s\n", chatApp.ServerID.PublicKey)
		}
	})

	// Graceful drain for history on SIGTERM/SIGINT to avoid losing last second of messages
	go func() {
		ch := make(chan os.Signal, 1)
		signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM)
		<-ch
		logInfo("🛑 Nhận tín hiệu dừng, đang flush history...")
		if chatApp.Chain.Store != nil {
			_ = chatApp.Chain.Store.Close()
		}
		os.Exit(0)
	}()

	server := &http.Server{
		Addr:              ":" + Cfg.Static.Port,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	logInfof("🚀 Server đang chạy tại port %v (version %s, instance %s)", Cfg.Static.Port, Version, Cfg.Static.Root)
	log.Fatal(server.ListenAndServe())
}

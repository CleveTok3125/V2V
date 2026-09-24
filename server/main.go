package main

import (
	"encoding/json"
	"fmt"
	"log"
	"mime"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
	_ "time/tzdata"

	"github.com/CleveTok3125/V2V/internal/guard"
	"github.com/CleveTok3125/V2V/internal/serverconfig"
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
	if s.Attack != nil {
		s.Attack.noteAttempt(clientIP)
	}

	if blocklisted(s.Blocklist, clientIP) {
		if s.Attack != nil {
			s.Attack.noteBlock()
		}
		logWarnf("⛔ [BLOCKLIST] Reject %s", trustedproxy.Clip(clientIP, 200))
		http.Error(w, "Blocked.", http.StatusForbidden)
		return
	}

	if Cfg.Static.RequireIPv4 && !isIPv4(clientIP) {
		logWarnf("⛔ [IPV6] Reject %s (REQUIRE_IPV4)", trustedproxy.Clip(clientIP, 200))
		http.Error(w, "IPv6 is not accepted here.", http.StatusForbidden)
		return
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

	if s.overCap() {
		if s.Attack != nil {
			s.Attack.note503()
		}
		w.Header().Set("Retry-After", "5")
		http.Error(w, "Server is full, retry later.", http.StatusServiceUnavailable)
		return
	}
	atomic.AddInt64(&s.Inflight, 1)

	if s.gateRequired() {
		if !s.checkGatePass(w, r, clientIP) {
			s.releaseInflight()
			return
		}
	}

	logInfof("🔌 New request | Client IP: %s | Proxy IP: %s | Via: %s trusted=%v | Upgrade: %s | %s\n", clientIP, r.RemoteAddr, outcome.Provider, outcome.Trusted, r.Header.Get("Upgrade"), proxyHeadersForLog(r))

	conn, err := s.Upgrader.Upgrade(w, r, nil)
	if err != nil {
		s.releaseInflight()
		logErrorf("❌ Upgrade error: %v", err)
		return
	}
	defer conn.Close()

	session, err := s.authenticateClient(conn, clientIP, clientHost(r), onionRequest(r))
	if err != nil {
		s.releaseInflight()
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
	s.releaseInflight()
	s.observeConnect(clientIP, session.DisplayName, sessionIdentityKind(session))

	// Anti-race re-check: a burst that passed the pre-upgrade cap
	// together must not all stay registered.
	if s.overCap() {
		s.Hub.unregisterClient(session, clientIP)
		s.observeDisconnect(clientIP)
		return
	}

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

// loadStaticConfig reads the static configuration for the active instance
// root and logs the non-fatal warnings returned by the shared loader.
func loadStaticConfig() (StaticConfig, error) {
	cfg, warns, err := serverconfig.LoadStaticConfig(ServerRoot)
	for _, w := range warns {
		logWarnf("%s", w)
	}
	return cfg, err
}

// loadDynamicConfig reads the hot-reloadable configuration and logs the
// non-fatal warnings returned by the shared loader.
func loadDynamicConfig() (DynamicConfig, error) {
	cfg, warns, err := serverconfig.LoadDynamicConfig()
	for _, w := range warns {
		logWarnf("%s", w)
	}
	return cfg, err
}

// loadAbuseConfig reads the abuse/PoW/behavior knobs and logs the
// non-fatal warnings returned by the shared loader.
func loadAbuseConfig() (serverconfig.AbuseConfig, error) {
	cfg, warns, err := serverconfig.LoadAbuseConfig()
	for _, w := range warns {
		logWarnf("%s", w)
	}
	return cfg, err
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

	newAbuse, err := loadAbuseConfig()
	if err != nil {
		logErrorf("❌ [HOT-RELOAD] Không thể nạp lại abuse config: %v", err)
		return
	}

	Cfg.Dynamic.Store(&newDynamic)
	Cfg.Abuse.Store(&newAbuse)
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

	initialAbuse, err := loadAbuseConfig()
	if err != nil {
		log.Fatalf("❌ CRITICAL ERROR: %v", err)
	}
	Cfg.Abuse.Store(&initialAbuse)

	if err := InitLogger(Cfg.Static.LogFilePath, Cfg.Static.MaxLogSizeMB); err != nil {
		log.Fatalf("❌ CRITICAL ERROR: %v", err)
	}

	chatApp := NewChatServer()
	blNets, blWarn, err := loadBlocklist(Cfg.Static.BlocklistFile)
	if err != nil {
		log.Fatalf("❌ CRITICAL ERROR: blocklist: %v", err)
	}
	if blWarn != "" {
		logWarnf("%s", blWarn)
	} else {
		logInfof("🛡️ Blocklist: %d nets from %s", len(blNets), Cfg.Static.BlocklistFile)
	}
	chatApp.Blocklist = blNets

	chatApp.Behavior, chatApp.Screener = initBehaviorEngines()
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
	go chatApp.attackSampler()
	go chatApp.behaviorScheduler()

	mux := http.NewServeMux()

	mime.AddExtensionType(".wasm", "application/wasm")
	webtermDir = resolveWebtermDir(executableDir())
	webHandler := http.StripPrefix("/web/", webFilesHandler(webtermDir))
	mux.HandleFunc("/web/", chatApp.instrumentHTTP("web_static", func(w http.ResponseWriter, r *http.Request) {
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
	}))

	LoadWebauthnEnv()
	mux.HandleFunc("/webauthn/enroll/begin", chatApp.instrumentHTTP("webauthn_begin", func(w http.ResponseWriter, r *http.Request) {
		if !passkeyAllowed(r) {
			logWarnf("⛔ [ONION] Chặn enroll từ %s (ONION_ALLOW_PASSKEY=false)", trustedproxy.Clip(r.Host, 200))
			http.NotFound(w, r)
			return
		}
		if rejectUntrustedProxy(w, r) {
			return
		}
		chatApp.handleEnrollBegin(w, r)
	}))
	mux.HandleFunc("/webauthn/enroll/finish", chatApp.instrumentHTTP("webauthn_finish", func(w http.ResponseWriter, r *http.Request) {
		if !passkeyAllowed(r) {
			logWarnf("⛔ [ONION] Chặn enroll từ %s (ONION_ALLOW_PASSKEY=false)", trustedproxy.Clip(r.Host, 200))
			http.NotFound(w, r)
			return
		}
		if rejectUntrustedProxy(w, r) {
			return
		}
		chatApp.handleEnrollFinish(w, r)
	}))

	mux.HandleFunc("/api/server_pubkey", chatApp.instrumentHTTP("meta", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		pub := ""
		if chatApp.ServerID != nil {
			pub = chatApp.ServerID.PublicKey
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"public_key": pub})
	}))

	mux.HandleFunc("/api/trip/verify", chatApp.instrumentHTTP("trip_verify", func(w http.ResponseWriter, r *http.Request) {
		if rejectUntrustedProxy(w, r) {
			return
		}
		chatApp.handleTripVerify(w, r)
	}))
	mux.HandleFunc("/api/version", chatApp.instrumentHTTP("meta", handleAPIVersion))

	mux.HandleFunc("/api/join-gate", chatApp.instrumentHTTP("join_gate", func(w http.ResponseWriter, r *http.Request) {
		chatApp.handleJoinGate(w, r)
	}))

	mux.HandleFunc("/", chatApp.instrumentHTTP("info", func(w http.ResponseWriter, r *http.Request) {
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
	}))

	// Graceful drain for history on SIGTERM/SIGINT to avoid losing last second of messages
	go func() {
		ch := make(chan os.Signal, 1)
		signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM)
		<-ch
		logInfo("🛑 Nhận tín hiệu dừng, đang flush history...")
		if chatApp.Chain.Store != nil {
			_ = chatApp.Chain.Store.Close()
		}
		if chatApp.Behavior != nil {
			if err := chatApp.Behavior.Save(); err != nil {
				logInfof("⚠️ behavior save: %v", err)
			}
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

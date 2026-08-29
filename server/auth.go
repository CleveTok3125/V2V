package main

import (
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"localchat/internal/filter"

	"github.com/gorilla/websocket"
)

func (s *ChatServer) HandleAuth(conn *websocket.Conn, clientIP, expectedHost string) (Permission, AuthPacket, error) {
	// Allow a small grace window beyond the 10s nonce TTL so slow clients
	// still fail cleanly instead of hanging the auth read forever.
	const authResponseTimeout = 12 * time.Second
	nonceBytes := make([]byte, 64)

	if _, err := rand.Read(nonceBytes); err != nil {
		return GetDefaultPermission(), AuthPacket{}, fmt.Errorf("auth_error: entropy_exhaustion")
	}
	nonceHex := hex.EncodeToString(nonceBytes)

	s.ActiveNonces.Store(nonceHex, NonceMeta{
		ExpiresAt: time.Now().Add(10 * time.Second),
		IP:        clientIP,
	})

	time.AfterFunc(11*time.Second, func() {
		s.ActiveNonces.Delete(nonceHex)
	})

	serverPub := ""
	serverSig := ""
	serverHost := expectedHost
	if s.ServerID != nil {
		serverPub = s.ServerID.PublicKey
		if privBytes, err := hex.DecodeString(s.ServerID.PrivateKey); err == nil && len(privBytes) == ed25519.PrivateKeySize {
			priv := ed25519.PrivateKey(privBytes)
			sig := ed25519.Sign(priv, []byte("V2V-SERVER-v1\x00"+nonceHex+"\x00"+serverHost))
			serverSig = hex.EncodeToString(sig)
		}
	}
	if err := conn.WriteJSON(AuthPacket{
		Type:         "auth_challenge",
		Nonce:        nonceHex,
		ServerPubKey: serverPub,
		ServerSig:    serverSig,
		ServerHost:   serverHost,
	}); err != nil {
		return GetDefaultPermission(), AuthPacket{}, err
	}

	conn.SetReadDeadline(time.Now().Add(authResponseTimeout))
	// Cap the pre-auth response size. Without a limit here the server would
	// buffer an arbitrarily large auth packet (the chat-phase ReadLimit is only
	// set later, in ReadPump) and could be memory-exhausted pre-authentication.
	conn.SetReadLimit(64 * 1024)
	var resp AuthPacket
	if err := conn.ReadJSON(&resp); err != nil {
		return GetDefaultPermission(), resp, err
	}
	conn.SetReadDeadline(time.Time{})

	perms := GetDefaultPermission()

	if utf8.RuneCountInString(resp.Username) > Cfg.Dynamic.Load().MaxUsernameLength {
		return perms, resp, fmt.Errorf("auth_error: payload_too_large")
	}

	if len(resp.Role) > 64 {
		return perms, resp, fmt.Errorf("auth_error: invalid_role_length")
	}

	// Validate the nonce for every client (guest or role) so replayed, expired
	// or cross-IP nonces are rejected uniformly.
	metaRaw, exists := s.ActiveNonces.LoadAndDelete(resp.Nonce)
	if !exists {
		log.Printf("⚠️ [AUTH ALERT] %s: Nonce không tồn tại hoặc đã bị sử dụng (Dấu hiệu Replay Attack).", clientIP)
		return perms, resp, fmt.Errorf("auth_error: invalid_nonce")
	}

	meta := metaRaw.(NonceMeta)

	if time.Now().After(meta.ExpiresAt) {
		log.Printf("⚠️ [AUTH FAIL] %s: Nonce đã hết hạn.", clientIP)
		return perms, resp, fmt.Errorf("auth_error: expired_nonce")
	}
	if meta.IP != clientIP {
		log.Printf("🚨 [SECURITY BREACH] %s đang cố sử dụng Nonce được cấp cho IP %s! (Dấu hiệu cướp Token/MITM).", clientIP, meta.IP)
		return perms, resp, fmt.Errorf("auth_error: ip_mismatch")
	}

	// WebAuthn passkey branch: an assertion in the packet is verified against
	// the passkeys[] identities of the claimed role, granting the exact same
	// Permission as a key-file identity would.
	if resp.PasskeyID != "" || resp.PasskeySig != "" {
		var lastErr error
		if !WAConfig.Enabled {
			log.Printf("⚠️ [AUTH FAIL] %s: passkey bị tắt (thiếu WEBAUTHN_RPID/ORIGIN).", clientIP)
			return perms, resp, fmt.Errorf("auth_error: passkey_disabled")
		}
		s.RoleRegistryMu.RLock()
		roleDef, exists := s.RoleRegistry[resp.Role]
		s.RoleRegistryMu.RUnlock()
		if !exists {
			return perms, resp, fmt.Errorf("auth_error: invalid_role")
		}
		// Candidate 1: hand-imported identities in roles.json (software
		// passkey from a desktop key.json). No server-side counter.
		for _, pk := range roleDef.Passkeys {
			if pk.CredentialID != resp.PasskeyID {
				continue
			}
			pub, err := base64.RawURLEncoding.DecodeString(pk.PublicKey)
			if err != nil {
				continue
			}
			if _, verr := verifyAssertion(pub, resp.Nonce, resp.PasskeyAuthData, resp.PasskeyClientData, resp.PasskeySig); verr == nil {
				resp.AuthType = "passkey_soft"
				log.Printf("✅ [AUTH SUCCESS] %s đăng nhập bằng passkey mềm, role: [%s]", clientIP, resp.Role)
				return roleDef.Permission, resp, nil
			} else {
				lastErr = verr
			}
		}

		// Candidate 2: real passkeys enrolled via the web ceremony. These
		// live in the managed store with a persisted sign counter, enabling
		// clone detection.
		if cred, ok := s.WebAuthn.Credential(resp.Role, resp.PasskeyID); ok {
			pub, err := base64.RawURLEncoding.DecodeString(cred.PublicKey)
			if err != nil {
				return perms, resp, fmt.Errorf("auth_error: verification_failed")
			}
			counter, verr := verifyAssertion(pub, resp.Nonce, resp.PasskeyAuthData, resp.PasskeyClientData, resp.PasskeySig)
			switch {
			case verr == nil && (counter == 0 || cred.SignCount == 0 || counter > cred.SignCount):
				if counter != 0 {
					_ = s.WebAuthn.UpdateSignCount(resp.Role, cred.CredentialID, counter)
				}
				resp.AuthType = "passkey"
				log.Printf("✅ [AUTH SUCCESS] %s đăng nhập bằng passkey thật, role: [%s]", clientIP, resp.Role)
				return roleDef.Permission, resp, nil
			case verr == nil:
				lastErr = errors.New("counter_not_increasing")
			default:
				lastErr = verr
			}
		}

		if lastErr != nil {
			log.Printf("🚨 [PASSKEY FAIL] %s: %v", clientIP, lastErr)
		}
		log.Printf("🚨 [BRUTE-FORCE ALERT] %s: assertion passkey không khớp credential nào của role [%s]!", clientIP, resp.Role)
		return perms, resp, fmt.Errorf("auth_error: verification_failed")
	}

	if resp.Role == "" {
		return perms, resp, nil
	}

	s.RoleRegistryMu.RLock()
	roleDef, exists := s.RoleRegistry[resp.Role]
	s.RoleRegistryMu.RUnlock()

	if !exists {
		log.Printf("⚠️ [AUTH FAIL] %s: Yêu cầu Role không tồn tại [%s]", clientIP, resp.Role)
		return perms, resp, fmt.Errorf("auth_error: invalid_role")
	}

	sig, err := hex.DecodeString(resp.Signature)
	if err != nil || len(sig) != ed25519.SignatureSize {
		log.Printf("🚨 [AUTH FAIL] %s: Signature sai định dạng cho role [%s]", clientIP, resp.Role)
		return perms, resp, fmt.Errorf("auth_error: invalid_signature")
	}

	// Server pubkey pinning: the server's public key is part of the signed payload
	// so a key cannot be reused across deployments with different server identities.
	// When the client has a pin, it must match the current server's pubkey.
	serverPub = ""
	if s.ServerID != nil {
		serverPub = s.ServerID.PublicKey
	}
	signedData := nonceHex + "|" + resp.Role + "|" + resp.Username + "|" + serverPub
	signedBytes := []byte(signedData)

	for _, id := range roleDef.Identities {
		pub, err := hex.DecodeString(id.PublicKey)
		if err != nil || len(pub) != ed25519.PublicKeySize {
			continue
		}
		if id.ServerPubKey != "" && !strings.EqualFold(strings.TrimSpace(id.ServerPubKey), serverPub) {
			srvShort := serverPub
			if len(srvShort) > 12 {
				srvShort = srvShort[:12]
			}
			pinShort := id.ServerPubKey
			if len(pinShort) > 12 {
				pinShort = pinShort[:12]
			}
			log.Printf("🚨 [AUTH FAIL] %s: identity pinned to server %q but this server is %q", clientIP, pinShort, srvShort)
			continue
		}

		if ed25519.Verify(pub, signedBytes, sig) {
			resp.IdentityPub = id.PublicKey
			h := hmac.New(sha512.New, []byte(id.HmacShield))
			h.Write(sig)
			h.Write([]byte(nonceHex))

			hmacBytes, err := hex.DecodeString(resp.Hmac)
			if err == nil && hmac.Equal(h.Sum(nil), hmacBytes) {
				s.alertConcurrentIdentity(resp.IdentityPub, clientIP)
				resp.AuthType = "ed25519"
				log.Printf("✅ [AUTH SUCCESS] %s đăng nhập thành công role: [%s]", clientIP, resp.Role)
				return roleDef.Permission, resp, nil
			}
		}
	}

	log.Printf("🚨 [BRUTE-FORCE ALERT] %s: Sai Key/HMAC khi cố lấy quyền [%s]!", clientIP, resp.Role)
	return perms, resp, fmt.Errorf("auth_error: verification_failed")
}

// To prevent IP spoofing, only accept IPs sent from Cloudflare
// Change this getClientIP function if you are not using Cloudflare
// clientHost extracts the lowercase hostname the client connected through.
func clientHost(r *http.Request) string {
	host := strings.ToLower(strings.TrimSpace(r.Host))
	if h, _, err := net.SplitHostPort(host); err == nil && h != "" {
		return h
	}
	return strings.Trim(host, "[]")
}

// alertConcurrentIdentity notifies an existing session when its identity
// logs in from somewhere else. MVP: notify only; kicking is opt-in later.
func (s *ChatServer) alertConcurrentIdentity(identityPubHex, newClientIP string) {
	raw, ok := s.ActiveIdentities.Load(identityPubHex)
	if !ok {
		return
	}
	prev, _ := raw.(*ClientSession)
	if prev == nil || prev.Conn == nil {
		return
	}
	s.ClientsMu.Lock()
	_, alive := s.Clients[prev.Conn]
	s.ClientsMu.Unlock()
	if !alive {
		return
	}
	select {
	case prev.Send <- []byte("\x1b[90m[He thong]: Danh tinh cua ban vua duoc dang nhap tu " + newClientIP + ".\x1b[0m"):
	default:
	}
	log.Printf("⚠️ [IDENTITY CONCURRENT] identity đăng nhập song song từ %s (phiên cũ còn sống)", newClientIP)
}

func getClientIP(r *http.Request) string {
	remoteIP, _, _ := net.SplitHostPort(r.RemoteAddr)
	if cfIP := r.Header.Get("CF-Connecting-IP"); cfIP != "" {
		return cfIP
	}
	return remoteIP
}

func (s *ChatServer) LoadRoles() {
	for _, p := range RolesFilePaths {
		data, err := os.ReadFile(p)
		if err == nil {
			var tempRegistry map[string]RoleDefinition
			if err := json.Unmarshal(data, &tempRegistry); err != nil {
				log.Printf("❌ [HOT-RELOAD LỖI] Cú pháp file %s không hợp lệ: %v. Đang giữ nguyên Roles cũ!", p, err)
				return
			}

			s.RoleRegistryMu.Lock()
			s.RoleRegistry = tempRegistry
			s.RoleRegistryMu.Unlock()

			log.Printf("✅ Đã nạp cấu hình quyền hạn (Roles) từ: %s", p)
			return
		}
	}
	log.Println("ℹ️ Không tìm thấy roles.json (Sẽ hoạt động với quyền User mặc định)")
}

func (s *ChatServer) CheckConnectionRate(w http.ResponseWriter, clientIP string) bool {
	s.AuthFailsMu.Lock()
	record := s.AuthFails[clientIP]

	if time.Now().Before(record.UnlockTime) {
		s.AuthFailsMu.Unlock()
		log.Printf("⛔ [BAN] Từ chối %s. Vui lòng đợi đến %s.", clientIP, record.UnlockTime.Format("15:04:05"))
		http.Error(w, "IP của bạn đang bị khóa tạm thời do xác thực sai nhiều lần.", http.StatusTooManyRequests)
		return false
	}
	s.AuthFailsMu.Unlock()

	s.LastConnectMu.Lock()
	if lastTime, exists := s.LastConnectTime[clientIP]; exists {
		if time.Since(lastTime) < Cfg.Dynamic.Load().ConnectionCooldown {
			s.LastConnectMu.Unlock()
			log.Printf("⛔ Từ chối: %s kết nối ra/vào quá nhanh.\n", clientIP)
			http.Error(w, "Bạn thao tác ra/vào quá nhanh! Vui lòng đợi vài giây rồi thử lại.", http.StatusTooManyRequests)
			return false
		}
	}
	s.LastConnectTime[clientIP] = time.Now()
	s.LastConnectMu.Unlock()

	return true
}

func (s *ChatServer) handleAuthPenalty(clientIP string) {
	s.AuthFailsMu.Lock()
	defer s.AuthFailsMu.Unlock()

	record := s.AuthFails[clientIP]
	record.FailCount++

	if record.FailCount >= 5 {
		record.UnlockTime = time.Now().Add(5 * time.Minute)
		record.FailCount = 0
	}
	s.AuthFails[clientIP] = record
}

func (s *ChatServer) generateDisplayName(username string, clientIP string, perms Permission) string {
	if err := filter.ValidateDisplayName(username); err != nil {
		log.Printf("⚠️ [FILTER] displayName invalid from %s: %v -> fallback Anonymous", clientIP, err)
		username = "Anonymous"
	}
	name := strings.TrimSpace(username)
	if name == "" {
		name = "Anonymous"
	}

	// Defensive cap: never let the sanitized name exceed the configured limit,
	// so oversized input cannot inflate every broadcast/history/log message.
	maxLen := Cfg.Dynamic.Load().MaxUsernameLength
	if maxLen > 0 && utf8.RuneCountInString(name) > maxLen {
		name = string([]rune(name)[:maxLen])
	}

	if perms.CustomPrefix != "" {
		return perms.CustomPrefix + name
	}

	hash := sha256.Sum256([]byte(clientIP))
	ipSuffix := hex.EncodeToString(hash[:])[:4]
	return fmt.Sprintf("%s#%s", name, ipSuffix)
}

func generateTripcode(secret string, length int) string {
	if secret == "" || length <= 0 {
		return ""
	}

	hashTrip := sha256.Sum256([]byte(secret))
	fullHex := hex.EncodeToString(hashTrip[:])

	if length > len(fullHex) {
		length = len(fullHex)
	}

	return "◆ " + fullHex[:length]
}

func (s *ChatServer) authenticateClient(conn *websocket.Conn, clientIP, expectedHost string) (*ClientSession, error) {
	perms, authPacket, err := s.HandleAuth(conn, clientIP, expectedHost)
	if err != nil {
		if authPacket.Role != "" {
			s.handleAuthPenalty(clientIP)
		}
		// Structured rejection: clients show the reason instead of a
		// cryptic JSON parse error on the close frame.
		_ = conn.WriteJSON(AuthPacket{Type: "auth_failed", Error: err.Error()})
		conn.Close()
		return nil, err
	}

	if len(authPacket.Tripcode) > Cfg.Dynamic.Load().MaxTripcodeLength {
		errMsg := fmt.Sprintf("[Hệ thống]: Mật khẩu Tripcode quá dài (tối đa %d byte). Bị từ chối!", Cfg.Dynamic.Load().MaxTripcodeLength)
		conn.WriteMessage(websocket.TextMessage, []byte(errMsg))
		conn.Close()
		log.Printf("⚠️ [AUTH FAIL] %s: Tripcode secret quá dài (%d bytes) - Từ chối để chống trùng lặp.", clientIP, len(authPacket.Tripcode))
		return nil, fmt.Errorf("auth_error: tripcode_too_long")
	}

	if authPacket.Role != "" {
		s.AuthFailsMu.Lock()
		delete(s.AuthFails, clientIP)
		s.AuthFailsMu.Unlock()
	}

	if authPacket.AuthType == "" {
		authPacket.AuthType = "guest"
	}
	finalUsername := s.generateDisplayName(authPacket.Username, clientIP, perms)
	err = conn.WriteJSON(AuthPacket{
		Type:     "auth_success",
		Username: finalUsername,
		Role:     authPacket.Role,
		AuthType: authPacket.AuthType,
		Perms:    &perms,
	})
	if err != nil {
		return nil, err
	}

	return &ClientSession{
		Conn:        conn,
		DisplayName: finalUsername,
		Tripcode:    generateTripcode(authPacket.Tripcode, 8),
		Perms:       perms,
		Send:        make(chan []byte, 256),
	}, nil
}

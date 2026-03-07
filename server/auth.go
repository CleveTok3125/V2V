package main

import (
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"time"
	"unicode/utf8"

	"github.com/gorilla/websocket"
)

func (s *ChatServer) HandleAuth(conn *websocket.Conn, clientIP string) (Permission, AuthPacket, error) {
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

	if err := conn.WriteJSON(AuthPacket{Type: "auth_challenge", Nonce: nonceHex}); err != nil {
		return GetDefaultPermission(), AuthPacket{}, err
	}

	var resp AuthPacket
	if err := conn.ReadJSON(&resp); err != nil {
		return GetDefaultPermission(), resp, err
	}

	perms := GetDefaultPermission()

	if resp.Role == "" {
		return perms, resp, nil
	}

	if len(resp.Role) > 64 {
		return perms, resp, fmt.Errorf("auth_error: invalid_role_length")
	}

	if utf8.RuneCountInString(resp.Username) > Cfg.MaxUsernameLength {
		return perms, resp, fmt.Errorf("auth_error: payload_too_large")
	}

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

	signedData := nonceHex + "|" + resp.Role + "|" + resp.Username
	signedBytes := []byte(signedData)

	for _, id := range roleDef.Identities {
		pub, err := hex.DecodeString(id.PublicKey)
		if err != nil || len(pub) != ed25519.PublicKeySize {
			continue
		}

		if ed25519.Verify(pub, signedBytes, sig) {
			h := hmac.New(sha512.New, []byte(id.HmacShield))
			h.Write(sig)
			h.Write([]byte(nonceHex))

			hmacBytes, err := hex.DecodeString(resp.Hmac)
			if err == nil && hmac.Equal(h.Sum(nil), hmacBytes) {
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
func getClientIP(r *http.Request) string {
	remoteIP, _, _ := net.SplitHostPort(r.RemoteAddr)
	if cfIP := r.Header.Get("CF-Connecting-IP"); cfIP != "" {
		return cfIP
	}
	return remoteIP
}

func (s *ChatServer) LoadRoles() {
	paths := []string{"./roles.json", "/etc/secrets/roles.json"}
	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err == nil {
			s.RoleRegistryMu.Lock()
			json.Unmarshal(data, &s.RoleRegistry)
			s.RoleRegistryMu.Unlock()
			log.Printf("✅ Đã tải cấu hình quyền hạn từ: %s", p)
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
		if time.Since(lastTime) < Cfg.ConnectionCooldown {
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
	name := sanitizeString(username)
	if name == "" {
		name = "Anonymous"
	}

	if perms.CustomPrefix != "" {
		return perms.CustomPrefix + name
	}

	hash := sha256.Sum256([]byte(clientIP))
	ipSuffix := hex.EncodeToString(hash[:])[:4]
	return fmt.Sprintf("%s#%s", name, ipSuffix)
}

func (s *ChatServer) authenticateClient(conn *websocket.Conn, clientIP string) (*ClientSession, error) {
	perms, authPacket, err := s.HandleAuth(conn, clientIP)
	if err != nil {
		if authPacket.Role != "" {
			s.handleAuthPenalty(clientIP)
			conn.WriteMessage(websocket.TextMessage, []byte("[Hệ thống]: Xác thực thất bại! Đã ngắt kết nối."))
		}
		conn.Close()
		return nil, err
	}

	if authPacket.Role != "" {
		s.AuthFailsMu.Lock()
		delete(s.AuthFails, clientIP)
		s.AuthFailsMu.Unlock()
	}

	return &ClientSession{
		Conn:        conn,
		DisplayName: s.generateDisplayName(authPacket.Username, clientIP, perms),
		Perms:       perms,
		Send:        make(chan []byte, 256),
	}, nil
}

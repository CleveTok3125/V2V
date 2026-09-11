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
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/CleveTok3125/V2V/internal/filter"
	"github.com/CleveTok3125/V2V/internal/guard"

	"github.com/gorilla/websocket"
)

func (s *ChatServer) HandleAuth(conn *websocket.Conn, clientIP, expectedHost string) (Permission, AuthPacket, error) {
	// Allow a small grace window beyond the 10s nonce TTL so slow clients
	// still fail cleanly instead of hanging the auth read forever.
	const authResponseTimeout = 12 * time.Second
	nonceBytes := make([]byte, 64)

	if _, err := rand.Read(nonceBytes); err != nil {
		return GetDefaultPermission(), AuthPacket{}, fmt.Errorf("%w", ErrEntropyExhaustion)
	}
	nonceHex := hex.EncodeToString(nonceBytes)

	s.ActiveNonces.Store(nonceHex, NonceMeta{
		ExpiresAt: time.Now().Add(10 * time.Second),
		IP:        clientIP,
	})

	// No per-nonce timer: a periodic sweep expires nonces instead, so a
	// pre-auth flood cannot pile up one timer goroutine per connection.

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

	if err := conn.SetReadDeadline(time.Now().Add(authResponseTimeout)); err != nil {
		conn.Close()
		return GetDefaultPermission(), AuthPacket{}, err
	}
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
		return perms, resp, fmt.Errorf("%w", ErrPayloadTooLarge)
	}

	if len(resp.Role) > 64 {
		return perms, resp, fmt.Errorf("%w", ErrInvalidRoleLength)
	}

	// Validate the nonce for every client (guest or role) so replayed, expired
	// or cross-IP nonces are rejected uniformly.
	metaRaw, exists := s.ActiveNonces.LoadAndDelete(resp.Nonce)
	if !exists {
		logWarnf("⚠️ [AUTH ALERT] %s: Nonce không tồn tại hoặc đã bị sử dụng (Dấu hiệu Replay Attack).", clientIP)
		return perms, resp, fmt.Errorf("%w", ErrInvalidNonce)
	}

	meta := metaRaw.(NonceMeta)

	if time.Now().After(meta.ExpiresAt) {
		logWarnf("⚠️ [AUTH FAIL] %s: Nonce đã hết hạn.", clientIP)
		return perms, resp, fmt.Errorf("%w", ErrExpiredNonce)
	}
	if meta.IP != clientIP {
		logErrorf("🚨 [SECURITY BREACH] %s đang cố sử dụng Nonce được cấp cho IP %s! (Dấu hiệu cướp Token/MITM).", clientIP, meta.IP)
		return perms, resp, fmt.Errorf("%w", ErrIPMismatch)
	}

	// WebAuthn passkey branch: an assertion in the packet is verified against
	// the passkeys[] identities of the claimed role, granting the exact same
	// Permission as a key-file identity would.
	if resp.PasskeyID != "" || resp.PasskeySig != "" {
		var lastErr error
		if !WAConfig.Enabled {
			logWarnf("⚠️ [AUTH FAIL] %s: passkey bị tắt (thiếu WEBAUTHN_RPID/ORIGIN).", clientIP)
			return perms, resp, fmt.Errorf("%w", ErrPasskeyDisabled)
		}
		s.RoleRegistryMu.RLock()
		roleDef, exists := s.RoleRegistry[resp.Role]
		s.RoleRegistryMu.RUnlock()
		if !exists {
			return perms, resp, fmt.Errorf("%w", ErrInvalidRole)
		}
		// Passkeys only come from the managed store (web ceremony
		// enrollment). Hand-imported roles.json credentials no longer
		// exist: every credential here carries a persisted sign
		// counter for clone detection.
		if s.WebAuthn == nil {
			return perms, resp, fmt.Errorf("%w", ErrVerificationFailed)
		}
		if cred, ok := s.WebAuthn.Credential(resp.Role, resp.PasskeyID); ok {
			pub, err := base64.RawURLEncoding.DecodeString(cred.PublicKey)
			if err != nil {
				return perms, resp, fmt.Errorf("%w", ErrVerificationFailed)
			}
			counter, verr := verifyAssertionLib(pub, resp.Nonce, resp.PasskeyAuthData, resp.PasskeyClientData, resp.PasskeySig, resp.PasskeyID, resp.Role)
			switch {
			case verr == nil && (counter == 0 || cred.SignCount == 0 || counter > cred.SignCount):
				if counter != 0 {
					_ = s.WebAuthn.UpdateSignCount(resp.Role, cred.CredentialID, counter)
				}
				resp.AuthType = "passkey"
				logInfof("✅ [AUTH SUCCESS] %s đăng nhập bằng passkey thật, role: [%s]", clientIP, resp.Role)
				return roleDef.Permission, resp, nil
			case verr == nil:
				lastErr = ErrCounterNotIncreasing
			default:
				lastErr = verr
			}
		}

		if lastErr != nil {
			logErrorf("🚨 [PASSKEY FAIL] %s: %v", clientIP, lastErr)
		}
		logErrorf("🚨 [BRUTE-FORCE ALERT] %s: assertion passkey không khớp credential nào của role [%s]!", clientIP, resp.Role)
		return perms, resp, fmt.Errorf("%w", ErrVerificationFailed)
	}

	if resp.Role == "" {
		return perms, resp, nil
	}

	s.RoleRegistryMu.RLock()
	roleDef, exists := s.RoleRegistry[resp.Role]
	s.RoleRegistryMu.RUnlock()

	if !exists {
		logWarnf("⚠️ [AUTH FAIL] %s: Yêu cầu Role không tồn tại [%s]", clientIP, resp.Role)
		return perms, resp, fmt.Errorf("%w", ErrInvalidRole)
	}

	sig, err := hex.DecodeString(resp.Signature)
	if err != nil || len(sig) != ed25519.SignatureSize {
		logErrorf("🚨 [AUTH FAIL] %s: Signature sai định dạng cho role [%s]", clientIP, resp.Role)
		return perms, resp, fmt.Errorf("%w", ErrInvalidSignature)
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
			logErrorf("🚨 [AUTH FAIL] %s: identity pinned to server %q but this server is %q", clientIP, pinShort, srvShort)
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
				logInfof("✅ [AUTH SUCCESS] %s đăng nhập thành công role: [%s]", clientIP, resp.Role)
				return roleDef.Permission, resp, nil
			}
		}
	}

	logErrorf("🚨 [BRUTE-FORCE ALERT] %s: Sai Key/HMAC khi cố lấy quyền [%s]!", clientIP, resp.Role)
	return perms, resp, fmt.Errorf("%w", ErrVerificationFailed)
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
	logWarnf("⚠️ [IDENTITY CONCURRENT] identity đăng nhập song song từ %s (phiên cũ còn sống)", newClientIP)
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
				logErrorf("❌ [HOT-RELOAD LỖI] Cú pháp file %s không hợp lệ: %v. Đang giữ nguyên Roles cũ!", p, err)
				return
			}

			s.RoleRegistryMu.Lock()
			s.RoleRegistry = tempRegistry
			s.RoleRegistryMu.Unlock()

			logInfof("✅ Đã nạp cấu hình quyền hạn (Roles) từ: %s", p)
			return
		}
	}
	logInfo("ℹ️ Không tìm thấy roles.json (Sẽ hoạt động với quyền User mặc định)")
}

func (s *ChatServer) CheckConnectionRate(w http.ResponseWriter, clientIP string) bool {
	s.AuthFailsMu.Lock()
	rec := s.AuthFails[clientIP]
	now := time.Now()
	if guard.IsBanned(rec, now) {
		s.AuthFailsMu.Unlock()
		logWarnf("⛔ [BAN] Từ chối %s. Vui lòng đợi đến %s.", clientIP, rec.UnlockTime.Format("15:04:05"))
		http.Error(w, "IP của bạn đang bị khóa tạm thời do xác thực sai nhiều lần.", http.StatusTooManyRequests)
		return false
	}
	s.AuthFailsMu.Unlock()

	s.LastConnectMu.Lock()
	if lastTime, exists := s.LastConnectTime[clientIP]; exists {
		if ok, _ := guard.CheckConnectionRate(now, rec, lastTime, Cfg.Dynamic.Load().ConnectionCooldown); !ok {
			s.LastConnectMu.Unlock()
			logWarnf("⛔ Từ chối: %s kết nối ra/vào quá nhanh.\n", clientIP)
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

	rec := s.AuthFails[clientIP]
	rec = guard.NextPenalty(rec, time.Now())
	s.AuthFails[clientIP] = rec
}

func (s *ChatServer) generateDisplayName(username string, clientIP string, perms Permission) string {
	if err := filter.ValidateDisplayName(username); err != nil {
		logWarnf("⚠️ [FILTER] displayName invalid from %s: %v -> fallback Anonymous", clientIP, err)
		username = "Anonymous"
	}
	name := strings.TrimSpace(username)
	if name == "" {
		name = "Anonymous"
	}

	// Defensive cap: never let the sanitized name exceed the configured limit,
	// so oversized input cannot inflate every broadcast/history/log message.
	if cfg := Cfg.Dynamic.Load(); cfg != nil {
		if maxLen := cfg.MaxUsernameLength; maxLen > 0 && utf8.RuneCountInString(name) > maxLen {
			name = string([]rune(name)[:maxLen])
		}
	}

	// Dynamic hash length based on active connections
	s.ClientsMu.RLock()
	n := len(s.Clients)
	s.ClientsMu.RUnlock()
	total := n + 1
	hashLen := 4
	if total > 800 {
		hashLen = 6
	} else if total > 100 {
		hashLen = 5
	}

	// Salted hash with per-session server salt (ephemeral, not persisted)
	var hashStr string
	if len(s.DisplaySalt) > 0 {
		mac := hmac.New(sha256.New, s.DisplaySalt)
		mac.Write([]byte(clientIP))
		sum := mac.Sum(nil)
		hashStr = hex.EncodeToString(sum)[:hashLen]
	} else {
		h := sha256.Sum256([]byte(clientIP))
		hashStr = hex.EncodeToString(h[:])[:hashLen]
	}

	// Always include hash, even for privileged roles (no exception)
	var baseDisplay string
	if perms.CustomPrefix != "" {
		baseDisplay = perms.CustomPrefix + name + "#" + hashStr
	} else {
		baseDisplay = name + "#" + hashStr
	}

	// Serial handling for duplicate hash (same baseDisplay already taken)
	s.DisplayNameCountMu.Lock()
	defer s.DisplayNameCountMu.Unlock()
	if _, exists := s.DisplayNameCount[baseDisplay]; !exists {
		s.DisplayNameCount[baseDisplay] = 1
		return baseDisplay
	}
	// Find next available serial: base-2, base-3, ...
	for serial := 2; serial < 1000; serial++ {
		candidate := fmt.Sprintf("%s-%d", baseDisplay, serial)
		if _, exists := s.DisplayNameCount[candidate]; !exists {
			s.DisplayNameCount[candidate] = 1
			return candidate
		}
	}
	// Fallback (should not happen): return base with timestamp
	return fmt.Sprintf("%s-%d", baseDisplay, time.Now().UnixNano()%1000)
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
		logWarnf("⚠️ [AUTH FAIL] %s: Tripcode secret quá dài (%d bytes) - Từ chối để chống trùng lặp.", clientIP, len(authPacket.Tripcode))
		return nil, fmt.Errorf("%w", ErrTripcodeTooLong)
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
	// Tripcode handling: new flow sends TripPub (derived pubkey), legacy sends Tripcode secret.
	var tripPub, tripBadge string
	var tripSeq uint32
	var tripPrev string
	if authPacket.TripPub != "" {
		// Validate pub hex is 64 chars (32 bytes)
		if b, err := hex.DecodeString(authPacket.TripPub); err == nil && len(b) == ed25519.PublicKeySize {
			tripPub = strings.ToLower(authPacket.TripPub)
			tripBadge = guard.TripBadgeFromPubHex(tripPub)
			if v, ok := s.TripChains.Load(tripPub); ok {
				if ch, ok := v.(TripChain); ok {
					tripSeq = ch.Seq
					if len(ch.PrevHash) > 0 {
						tripPrev = hex.EncodeToString(ch.PrevHash)
					} else {
						tripPrev = hex.EncodeToString(make([]byte, 32))
					}
				}
			} else {
				tripPrev = hex.EncodeToString(make([]byte, 32))
			}
		}
	}
	err = conn.WriteJSON(AuthPacket{
		Type:     "auth_success",
		Username: finalUsername,
		Role:     authPacket.Role,
		AuthType: authPacket.AuthType,
		Perms:    &perms,
		TripPub:  tripPub,
		TripSeq:  tripSeq,
		TripPrev: tripPrev,
	})
	if err != nil {
		return nil, err
	}

	// Prefer new pub-based badge, fallback to legacy hash
	badge := tripBadge
	if badge == "" {
		badge = guard.GenerateTripcode(authPacket.Tripcode, 8)
	}
	return &ClientSession{
		Conn:        conn,
		DisplayName: finalUsername,
		Tripcode:    badge,
		TripPub:     tripPub,
		TripBadge:   badge,
		Host:        expectedHost,
		Perms:       perms,
		WantJoins:   authPacket.HistoryJoins,
		Send:        make(chan []byte, 256),
	}, nil
}

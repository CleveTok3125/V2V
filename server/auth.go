package main

import (
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha512"
	"encoding/hex"
	"fmt"
	"log"
	"time"

	"github.com/gorilla/websocket"
)

func HandleAuth(conn *websocket.Conn, clientIP string) (Permission, AuthPacket, error) {
	nonceBytes := make([]byte, 64)
	rand.Read(nonceBytes)
	nonceHex := hex.EncodeToString(nonceBytes)

	ActiveNonces.Store(nonceHex, NonceMeta{
		ExpiresAt: time.Now().Add(10 * time.Second),
		IP:        clientIP,
	})

	go func(n string) {
		time.Sleep(11 * time.Second)
		ActiveNonces.Delete(n)
	}(nonceHex)

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

	metaRaw, exists := ActiveNonces.LoadAndDelete(resp.Nonce)
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

	roleDef, exists := RoleRegistry[resp.Role]
	if !exists {
		log.Printf("⚠️ [AUTH FAIL] %s: Yêu cầu Role không tồn tại [%s]", clientIP, resp.Role)
		return perms, resp, fmt.Errorf("auth_error: invalid_role")
	}

	sig, err := hex.DecodeString(resp.Signature)
	if err != nil || len(sig) != ed25519.SignatureSize {
		log.Printf("🚨 [AUTH FAIL] %s: Signature sai định dạng cho role [%s]", clientIP, resp.Role)
		return perms, resp, fmt.Errorf("auth_error: invalid_signature")
	}

	signedData := append([]byte(nonceHex), []byte(resp.Role)...)
	signedData = append(signedData, []byte(resp.Username)...)
	isAuthenticated := false

	for _, id := range roleDef.Identities {
		pub, _ := hex.DecodeString(id.PublicKey)
		if ed25519.Verify(pub, signedData, sig) {
			h := hmac.New(sha512.New, []byte(id.HmacShield))
			h.Write(sig)
			h.Write([]byte(nonceHex))

			if hmac.Equal(h.Sum(nil), hexToBytes(resp.Hmac)) {
				perms = roleDef.Permission
				isAuthenticated = true
				break
			}
		}
	}

	if isAuthenticated {
		log.Printf("✅ [AUTH SUCCESS] %s đăng nhập thành công role: [%s]", clientIP, resp.Role)
		return perms, resp, nil
	} else {
		log.Printf("🚨 [BRUTE-FORCE ALERT] %s: Sai Key/HMAC khi cố lấy quyền [%s]!", clientIP, resp.Role)
		return perms, resp, fmt.Errorf("auth_error: verification_failed")
	}
}

func hexToBytes(s string) []byte {
	b, _ := hex.DecodeString(s)
	return b
}

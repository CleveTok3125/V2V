package main

import (
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha512"
	"encoding/hex"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

var activeNonces sync.Map

func HandleAuth(conn *websocket.Conn) (Permission, AuthPacket, error) {
	nonceBytes := make([]byte, 64)
	rand.Read(nonceBytes)
	nonceHex := hex.EncodeToString(nonceBytes)
	activeNonces.Store(nonceHex, time.Now().Add(10*time.Second))
	defer activeNonces.Delete(nonceHex)

	conn.WriteJSON(AuthPacket{Type: "auth_challenge", Nonce: nonceHex})

	var resp AuthPacket
	if err := conn.ReadJSON(&resp); err != nil {
		return GetDefaultPermission(), resp, err
	}

	perms := GetDefaultPermission()

	if resp.Role != "" && resp.Signature != "" {
		if roleDef, exists := RoleRegistry[resp.Role]; exists {
			signedData := append([]byte(nonceHex), []byte(resp.Role)...)
			sig, _ := hex.DecodeString(resp.Signature)

			for _, id := range roleDef.Identities {
				pub, _ := hex.DecodeString(id.PublicKey)
				if ed25519.Verify(pub, signedData, sig) {
					h := hmac.New(sha512.New, []byte(id.HmacShield))
					h.Write(sig)
					h.Write([]byte(nonceHex))

					if hmac.Equal(h.Sum(nil), hexToBytes(resp.Hmac)) {
						perms = roleDef.Permission
						break
					}
				}
			}
		}
	}

	return perms, resp, nil
}

func hexToBytes(s string) []byte {
	b, _ := hex.DecodeString(s)
	return b
}

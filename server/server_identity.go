package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"os"
)

func LoadOrCreateServerIdentity(path string) (*ServerIdentity, error) {
	if data, err := os.ReadFile(path); err == nil && len(data) > 0 {
		var s ServerIdentity
		if err := json.Unmarshal(data, &s); err == nil && s.PrivateKey != "" && s.PublicKey != "" {
			// Validate keypair
			priv, err := hex.DecodeString(s.PrivateKey)
			if err == nil && len(priv) == ed25519.PrivateKeySize {
				pub, err2 := hex.DecodeString(s.PublicKey)
				if err2 == nil && len(pub) == ed25519.PublicKeySize {
					privKey := ed25519.PrivateKey(priv)
					if pub != nil && string(pub) == string(privKey.Public().(ed25519.PublicKey)) {
						log.Printf("🔑 Đã nạp Server identity từ %s (pub %s…)", path, s.PublicKey[:16])
						return &s, nil
					}
				}
			}
			log.Printf("⚠️ Server identity file %s không hợp lệ, tạo mới", path)
		}
	}
	// Create new
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	s := &ServerIdentity{
		PublicKey:  hex.EncodeToString(pub),
		PrivateKey: hex.EncodeToString(priv),
	}
	data, _ := json.MarshalIndent(s, "", "  ")
	dir := "data"
	_ = os.MkdirAll(dir, 0o700)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return nil, err
	}
	if err := os.Rename(tmp, path); err != nil {
		return nil, err
	}
	log.Printf("🔑 Đã tạo Server identity mới tại %s (pub %s…)", path, s.PublicKey[:16])
	// Also print for admin to pin in key files
	fmt.Printf("🔑 Server public key (để pin trong key.json): %s\n", s.PublicKey)
	return s, nil
}

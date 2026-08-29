package main

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"localchat/internal/tripcolor"
)

func (s *ChatServer) handleTripVerify(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	// Abuse mitigation: cap query size and rate-limit per IP (ed25519 verify is cheap but still CPU)
	if len(r.URL.RawQuery) > 2048 {
		http.Error(w, "query too large", http.StatusRequestEntityTooLarge)
		return
	}
	clientIP := getClientIP(r)
	// Simple per-IP cooldown: 200ms (same as MessageCooldown) to prevent tight loops
	s.TripVerifyLastMu.Lock()
	if last, ok := s.TripVerifyLast[clientIP]; ok && time.Since(last) < 200*time.Millisecond {
		s.TripVerifyLastMu.Unlock()
		log.Printf("⛔ [TRIP VERIFY RATE] %s bị hạn chế", clientIP)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		json.NewEncoder(w).Encode(map[string]any{"valid": false, "error": "rate limited, slow down"})
		return
	}
	s.TripVerifyLast[clientIP] = time.Now()
	// Opportunistic cleanup of old entries to avoid memory growth
	if len(s.TripVerifyLast) > 1000 {
		for ip, t := range s.TripVerifyLast {
			if time.Since(t) > 10*time.Minute {
				delete(s.TripVerifyLast, ip)
			}
		}
	}
	s.TripVerifyLastMu.Unlock()

	pubHex := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("pub")))
	seqStr := r.URL.Query().Get("seq")
	prevHex := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("prev")))
	sigHex := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("sig")))
	msgHashHex := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("msg_hash")))
	serverPub := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("server_pub")))

	if pubHex == "" || sigHex == "" || prevHex == "" || msgHashHex == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]any{"valid": false, "error": "missing pub/seq/prev/sig/msg_hash"})
		return
	}
	// Early length check before hex.Decode to avoid large allocations
	if len(pubHex) != 64 || len(sigHex) != 128 || len(prevHex) != 64 || len(msgHashHex) != 64 {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"valid": false, "error": "invalid hex length"})
		return
	}
	if len(serverPub) != 0 && len(serverPub) != 64 {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"valid": false, "error": "invalid server_pub length"})
		return
	}
	seq64, err := strconv.ParseUint(seqStr, 10, 32)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]any{"valid": false, "error": "invalid seq"})
		return
	}
	seq := uint32(seq64)

	pubBytes, err1 := hex.DecodeString(pubHex)
	sigBytes, err2 := hex.DecodeString(sigHex)
	prevBytes, err3 := hex.DecodeString(prevHex)
	msgHashBytes, err4 := hex.DecodeString(msgHashHex)
	if err1 != nil || err2 != nil || err3 != nil || err4 != nil || len(pubBytes) != ed25519.PublicKeySize || len(sigBytes) != ed25519.SignatureSize || len(prevBytes) != 32 || len(msgHashBytes) != 32 {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"valid": false, "error": "invalid hex length"})
		return
	}
	if serverPub == "" && s.ServerID != nil {
		serverPub = strings.ToLower(s.ServerID.PublicKey)
	}
	payload := tripcolor.CanonicalPayload(serverPub, seq, prevBytes, msgHashBytes, pubBytes)
	valid := ed25519.Verify(pubBytes, payload, sigBytes)
	badge := ""
	if pubHex != "" {
		h := sha256.Sum256(pubBytes)
		badge = "◆ " + hex.EncodeToString(h[:])[:8]
	}
	color := ""
	if badge != "" {
		color = tripcolor.BadgeColor(badge)
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(w).Encode(map[string]any{
		"valid":      valid,
		"badge":      badge,
		"badgeColor": color,
		"pub":        pubHex,
		"seq":        seq,
		"server_pub": serverPub,
	})
}

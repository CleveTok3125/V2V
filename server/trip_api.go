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

	"github.com/CleveTok3125/V2V/internal/guard"
	"github.com/CleveTok3125/V2V/internal/tripcolor"
)

var guardTripCooldown = guard.NewCooldownMap()

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
	// Use shared CooldownMap via guard logic (200ms)
	if !guardTripCooldown.Allow(clientIP, 200*time.Millisecond) {
		log.Printf("⛔ [TRIP VERIFY RATE] %s bị hạn chế", clientIP)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		json.NewEncoder(w).Encode(map[string]any{"valid": false, "error": "rate limited, slow down"})
		return
	}

	pubHex := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("pub")))
	seqStr := r.URL.Query().Get("seq")
	prevHex := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("prev")))
	sigHex := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("sig")))
	msgHashHex := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("msg_hash")))
	queryServerPub := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("server_pub")))
	displayName := r.URL.Query().Get("display_name")
	textParam := r.URL.Query().Get("text")
	var tmpID uint64
	if v, err := strconv.ParseUint(r.URL.Query().Get("tmp_id"), 10, 64); err == nil {
		tmpID = v
	}
	// Enforce server_pub is server's own key; ignore query if it doesn't match.
	serverPub := ""
	if s.ServerID != nil {
		serverPub = strings.ToLower(s.ServerID.PublicKey)
	}
	if queryServerPub != "" && queryServerPub != serverPub {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"valid": false, "error": "server_pub mismatch"})
		return
	}

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

	// If text is provided, verify it matches msg_hash
	if textParam != "" {
		h := sha256.Sum256([]byte(textParam))
		if hex.EncodeToString(h[:]) != msgHashHex {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"valid": false, "error": "msg_hash does not match text"})
			return
		}
	}

	pubBytes, err1 := hex.DecodeString(pubHex)
	sigBytes, err2 := hex.DecodeString(sigHex)
	prevBytes, err3 := hex.DecodeString(prevHex)
	msgHashBytes, err4 := hex.DecodeString(msgHashHex)
	if err1 != nil || err2 != nil || err3 != nil || err4 != nil || len(pubBytes) != ed25519.PublicKeySize || len(sigBytes) != ed25519.SignatureSize || len(prevBytes) != 32 || len(msgHashBytes) != 32 {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"valid": false, "error": "invalid hex length"})
		return
	}
	payload := tripcolor.CanonicalPayload(serverPub, seq, prevBytes, msgHashBytes, pubBytes, displayName, tmpID)
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

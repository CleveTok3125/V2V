package main

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"localchat/internal/tripcolor"
)

func (s *ChatServer) handleTripVerify(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
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

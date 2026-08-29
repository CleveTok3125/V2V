package main

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"localchat/internal/tripcolor"

	"localchat/internal/filter"

	"github.com/gorilla/websocket"
)

func (s *ChatServer) appendMessageToHistory(msg string) {
	s.HistoryMu.Lock()
	defer s.HistoryMu.Unlock()

	msgSize := len(msg)
	s.ChatHistory = append(s.ChatHistory, msg)
	s.ChatHistorySize += msgSize

	for s.ChatHistorySize > Cfg.Dynamic.Load().MaxHistoryBytes && len(s.ChatHistory) > 0 {
		oldestSize := len(s.ChatHistory[0])
		s.ChatHistorySize -= oldestSize

		s.ChatHistory[0] = ""
		s.ChatHistory = s.ChatHistory[1:]
	}
}

func (s *ChatServer) AddMessageToHistory(msg string) {
	s.appendMessageToHistory(msg)
	if s.HistoryStore != nil {
		s.HistoryStore.Enqueue(msg, time.Now().In(Cfg.Static.Timezone))
	}
}

func (s *ChatServer) AddMessageWithTrip(msg string, trip *TripMeta) {
	s.appendMessageToHistory(msg)
	if s.HistoryStore != nil {
		s.HistoryStore.EnqueueWithTrip(msg, trip, time.Now().In(Cfg.Static.Timezone))
	}
}

func (s *ChatServer) InitHistoryStore(path string, maxSizeMB int) error {
	store, err := NewHistoryStore(path, maxSizeMB)
	if err != nil {
		return err
	}

	s.HistoryStore = store

	if store == nil {
		return nil
	}

	records, err := store.LoadRecords()
	if err != nil {
		return fmt.Errorf("không thể nạp history từ disk: %w", err)
	}

	for _, rec := range records {
		// Handle both new Wire and legacy Message
		var msgForHistory string
		var tripForChain *TripMeta
		var wireForVerify *WireMessage
		if rec.Wire != nil {
			data, _ := json.Marshal(rec.Wire)
			msgForHistory = string(data)
			tripForChain = rec.Wire.Trip
			wireForVerify = rec.Wire
		} else {
			msgForHistory = rec.Message
			tripForChain = rec.Trip
			// Try to parse Message as WireMessage for displayName/msg integrity check
			var w WireMessage
			if err := json.Unmarshal([]byte(rec.Message), &w); err == nil && w.Type == "chat" && w.Trip != nil {
				wireForVerify = &w
				tripForChain = w.Trip
			}
		}
		s.appendMessageToHistory(msgForHistory)
		if tripForChain != nil && tripForChain.Pub != "" && wireForVerify != nil {
			// Verify integrity before repopulating chain: check msg hash and signature with displayName
			actualHash := sha256.Sum256([]byte(wireForVerify.Text))
			if strings.ToLower(tripForChain.MsgHash) != hex.EncodeToString(actualHash[:]) {
				log.Printf("⚠️ [HISTORY TAMPER] %s seq %d: msg_hash mismatch (stored %s vs actual %s)", tripForChain.Pub[:12], tripForChain.Seq, tripForChain.MsgHash, hex.EncodeToString(actualHash[:]))
				continue
			}
			prevBytes, _ := hex.DecodeString(tripForChain.Prev)
			sigBytes, _ := hex.DecodeString(tripForChain.Sig)
			pubBytes, _ := hex.DecodeString(tripForChain.Pub)
			hashBytes, _ := hex.DecodeString(tripForChain.MsgHash)
			serverPub := tripForChain.ServerPub
			if serverPub == "" && s.ServerID != nil {
				serverPub = s.ServerID.PublicKey
			}
			displayName := wireForVerify.DisplayName
			if displayName == "" {
				displayName = tripForChain.DisplayName
			}
			payload := tripcolor.CanonicalPayload(serverPub, tripForChain.Seq, prevBytes, hashBytes, pubBytes, displayName)
			if len(pubBytes) != 32 || len(sigBytes) != 64 || !ed25519.Verify(pubBytes, payload, sigBytes) {
				log.Printf("⚠️ [HISTORY TAMPER] %s seq %d: signature invalid for displayName %q", tripForChain.Pub[:12], tripForChain.Seq, displayName)
				continue
			}
			if len(prevBytes) == 32 {
				h := sha256.New()
				h.Write(prevBytes)
				h.Write(sigBytes)
				h.Write(hashBytes)
				newPrev := h.Sum(nil)
				s.TripChains.Store(tripForChain.Pub, TripChain{Seq: tripForChain.Seq, PrevHash: newPrev})
			} else if tripForChain.Seq > 0 {
				s.TripChains.Store(tripForChain.Pub, TripChain{Seq: tripForChain.Seq, PrevHash: []byte{}})
			}
		} else if tripForChain != nil && tripForChain.Pub != "" {
			// Fallback for records without Wire (legacy) — keep old repopulate without displayName check
			prevBytes, _ := hex.DecodeString(tripForChain.Prev)
			if len(prevBytes) == 32 {
				sigBytes, _ := hex.DecodeString(tripForChain.Sig)
				msgHashBytes, _ := hex.DecodeString(tripForChain.MsgHash)
				if len(sigBytes) == 64 && len(msgHashBytes) == 32 {
					h := sha256.New()
					h.Write(prevBytes)
					h.Write(sigBytes)
					h.Write(msgHashBytes)
					newPrev := h.Sum(nil)
					s.TripChains.Store(tripForChain.Pub, TripChain{Seq: tripForChain.Seq, PrevHash: newPrev})
				} else {
					s.TripChains.Store(tripForChain.Pub, TripChain{Seq: tripForChain.Seq, PrevHash: prevBytes})
				}
			} else if tripForChain.Seq > 0 {
				s.TripChains.Store(tripForChain.Pub, TripChain{Seq: tripForChain.Seq, PrevHash: []byte{}})
			}
		}
	}

	loggedCount := len(s.ChatHistory)
	if loggedCount > 0 {
		log.Printf("📚 Đã phục hồi %d tin nhắn history từ disk", loggedCount)
	}

	return nil
}

func (s *ChatServer) Broadcast(message string, sender *websocket.Conn) {
	s.AddMessageToHistory(message)
	msgBytes := []byte(message)

	s.ClientsMu.RLock()
	defer s.ClientsMu.RUnlock()

	for conn, client := range s.Clients {
		if conn != sender {
			select {
			case client.Send <- msgBytes:
			default:
			}
		}
	}
}

func (s *ChatServer) BroadcastWithTrip(message string, trip *TripMeta, sender *websocket.Conn) {
	s.AddMessageWithTrip(message, trip)
	msgBytes := []byte(message)

	s.ClientsMu.RLock()
	defer s.ClientsMu.RUnlock()

	for conn, client := range s.Clients {
		if conn != sender {
			select {
			case client.Send <- msgBytes:
			default:
			}
		}
	}
}

func (s *ChatServer) BroadcastWire(wire WireMessage, sender *websocket.Conn) {
	data, _ := json.Marshal(wire)
	s.AddWireMessageToHistory(wire)
	s.ClientsMu.RLock()
	defer s.ClientsMu.RUnlock()
	for conn, client := range s.Clients {
		if conn != sender {
			select {
			case client.Send <- data:
			default:
			}
		}
	}
}

func (s *ChatServer) AddWireMessageToHistory(wire WireMessage) {
	// Store as JSON string for history (structured)
	data, _ := json.Marshal(wire)
	msgStr := string(data)
	s.appendMessageToHistory(msgStr)
	if s.HistoryStore != nil {
		// Store with Trip meta for chain repopulation
		s.HistoryStore.EnqueueWithTrip(msgStr, wire.Trip, time.Now().In(Cfg.Static.Timezone))
	}
}

func (s *ChatServer) CheckAndBroadcastDate(now time.Time) {
	currentDate := now.Format("02/01/2006")

	s.LastMessageDateMu.Lock()
	defer s.LastMessageDateMu.Unlock()

	if s.LastMessageDate == "" || s.LastMessageDate != currentDate {
		s.LastMessageDate = currentDate

		dateMsg := fmt.Sprintf("\x1b[36m--- Ngày %s ---\x1b[0m", currentDate)

		s.Broadcast(dateMsg, nil)
	}
}

func (s *ChatServer) SendChatHistory(session *ClientSession) {
	s.HistoryMu.RLock()

	historyLen := len(s.ChatHistory)

	if historyLen == 0 {
		s.HistoryMu.RUnlock()
		return
	}

	dynCfg := Cfg.Dynamic.Load()

	startIndex := 0
	if historyLen > dynCfg.MaxHistorySend {
		startIndex = historyLen - dynCfg.MaxHistorySend
	}

	historyCopy := make([]string, historyLen-startIndex)
	copy(historyCopy, s.ChatHistory[startIndex:])
	s.HistoryMu.RUnlock()

	session.Send <- []byte("--- Lịch sử chat gần đây ---")
	for _, msgStr := range historyCopy {
		// Keep history messages as stored (could be legacy ANSI string or WireMessage JSON)
		// For WireMessage JSON, send as is; for legacy, clean and send
		var wire WireMessage
		if err := json.Unmarshal([]byte(msgStr), &wire); err == nil && wire.Type == "chat" {
			session.Send <- []byte(msgStr)
		} else {
			cleaned := filter.CleanHistoryMessage(msgStr)
			session.Send <- []byte(cleaned)
		}
	}
	session.Send <- []byte("--- Kết thúc lịch sử ---")
}

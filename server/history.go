package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/CleveTok3125/V2V/internal/filter"
	"github.com/CleveTok3125/V2V/internal/trip"

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
	// Shrink underlying array when cap bloats >4*len to avoid holding 20MiB when only 5MiB needed
	if cap(s.ChatHistory) > 4*len(s.ChatHistory) && cap(s.ChatHistory) > 1024 {
		newCap := len(s.ChatHistory)
		if newCap < 1024 {
			newCap = 1024
		}
		n := make([]string, len(s.ChatHistory), newCap)
		copy(n, s.ChatHistory)
		s.ChatHistory = n
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
			tripForChain = nil
			wireForVerify = nil
		}
		s.appendMessageToHistory(msgForHistory)
		if tripForChain != nil && tripForChain.Pub != "" && wireForVerify != nil {
			textForHash := wireForVerify.Text
			displayName := wireForVerify.DisplayName
			if displayName == "" {
				displayName = tripForChain.DisplayName
			}
			serverPub := tripForChain.ServerPub
			if serverPub == "" && s.ServerID != nil {
				serverPub = s.ServerID.PublicKey
			}
			// For wire case, set Text field so trip.Verify recomputes correctly
			verifyText := textForHash
			if wireForVerify != nil {
				verifyText = wireForVerify.Text
			}
			_, err := trip.Verify(trip.VerifyParams{
				Text:        verifyText,
				DisplayName: displayName,
				ServerPub:   serverPub,
				PubHex:      tripForChain.Pub,
				Seq:         tripForChain.Seq,
				PrevHex:     tripForChain.Prev,
				SigHex:      tripForChain.Sig,
				MsgHashHex:  tripForChain.MsgHash,
				TmpID:       tripForChain.TmpID,
			})
			if err != nil {
				log.Printf("⚠️ [HISTORY TAMPER] %s seq %d: %v", tripForChain.Pub[:12], tripForChain.Seq, err)
				continue
			}
			// Success: derive newPrev via result
			prevBytes, _ := hex.DecodeString(tripForChain.Prev)
			sigBytes, _ := hex.DecodeString(tripForChain.Sig)
			hashBytes, _ := hex.DecodeString(tripForChain.MsgHash)
			h := sha256.New()
			h.Write(prevBytes)
			h.Write(sigBytes)
			h.Write(hashBytes)
			newPrev := h.Sum(nil)
			s.TripChains.Store(tripForChain.Pub, TripChain{Seq: tripForChain.Seq, PrevHash: newPrev})
		}
	}

	loggedCount := len(s.ChatHistory)
	if loggedCount > 0 {
		log.Printf("📚 Đã phục hồi %d tin nhắn history từ disk", loggedCount)
	}

	return nil
}

func (s *ChatServer) sendWithRetry(conn *websocket.Conn, client *ClientSession, msg []byte, isSystem bool) {
	// System/date messages get one retry to avoid drift when burst follows
	select {
	case client.Send <- msg:
	default:
		if isSystem {
			time.Sleep(20 * time.Millisecond)
			select {
			case client.Send <- msg:
			default:
			}
		}
	}
}

func (s *ChatServer) Broadcast(message string, sender *websocket.Conn) {
	s.AddMessageToHistory(message)
	msgBytes := []byte(message)

	s.ClientsMu.RLock()
	defer s.ClientsMu.RUnlock()

	for conn, client := range s.Clients {
		if conn != sender {
			s.sendWithRetry(conn, client, msgBytes, true)
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
			s.sendWithRetry(conn, client, msgBytes, true)
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
			s.sendWithRetry(conn, client, data, false)
		}
	}
	// Echo back to the sender as delivery confirmation so it can replace
	// its grey placeholder with the confirmed rendering.
	if sender != nil {
		if sess, ok := s.Clients[sender]; ok {
			select {
			case sess.Send <- data:
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
		s.HistoryStore.EnqueueWire(wire, time.Now().In(Cfg.Static.Timezone))
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

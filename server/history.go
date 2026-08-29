package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"
	"strings"
	"time"

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
		s.appendMessageToHistory(rec.Message)
		if rec.Trip != nil && rec.Trip.Pub != "" {
			// Repopulate TripChains with latest record per pub
			prevBytes, _ := hex.DecodeString(rec.Trip.Prev)
			if len(prevBytes) == 32 {
				// next prev after this record is hash(prev|sig|msgHash)
				sigBytes, _ := hex.DecodeString(rec.Trip.Sig)
				msgHashBytes, _ := hex.DecodeString(rec.Trip.MsgHash)
				if len(sigBytes) == 64 && len(msgHashBytes) == 32 {
					h := sha256.New()
					h.Write(prevBytes)
					h.Write(sigBytes)
					h.Write(msgHashBytes)
					newPrev := h.Sum(nil)
					s.TripChains.Store(rec.Trip.Pub, TripChain{Seq: rec.Trip.Seq, PrevHash: newPrev})
				} else {
					s.TripChains.Store(rec.Trip.Pub, TripChain{Seq: rec.Trip.Seq, PrevHash: prevBytes})
				}
			} else if rec.Trip.Seq > 0 {
				s.TripChains.Store(rec.Trip.Pub, TripChain{Seq: rec.Trip.Seq, PrevHash: []byte{}})
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

	for i := range historyCopy {
		historyCopy[i] = filter.CleanHistoryMessage(historyCopy[i])
	}

	combinedHistory := strings.Join(historyCopy, "\n")

	session.Send <- []byte("--- Lịch sử chat gần đây ---\n" + combinedHistory + "\n--- Kết thúc lịch sử ---")
}

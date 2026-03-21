package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

func (s *ChatServer) AddMessageToHistory(msg string) {
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

	combinedHistory := strings.Join(historyCopy, "\n")

	session.Send <- []byte("--- Lịch sử chat gần đây ---\n" + combinedHistory + "\n--- Kết thúc lịch sử ---")
}

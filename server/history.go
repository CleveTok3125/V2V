package main

import (
	"fmt"
	"time"

	"github.com/gorilla/websocket"
)

func (s *ChatServer) AddMessageToHistory(msg string) {
	s.HistoryMu.Lock()
	defer s.HistoryMu.Unlock()

	msgSize := len(msg)
	s.ChatHistory = append(s.ChatHistory, msg)
	s.ChatHistorySize += msgSize

	for s.ChatHistorySize > Cfg.MaxHistoryBytes && len(s.ChatHistory) > 0 {
		oldestSize := len(s.ChatHistory[0])
		s.ChatHistorySize -= oldestSize

		s.ChatHistory[0] = ""
		s.ChatHistory = s.ChatHistory[1:]
	}
}

func (s *ChatServer) Broadcast(message string, sender *websocket.Conn) {
	s.AddMessageToHistory(message)

	s.ClientsMu.Lock()
	defer s.ClientsMu.Unlock()

	for conn := range s.Clients {
		if conn != sender {
			if err := conn.WriteMessage(websocket.TextMessage, []byte(message)); err != nil {
				conn.Close()
				delete(s.Clients, conn)
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

func (s *ChatServer) SendChatHistory(conn *websocket.Conn) {
	s.HistoryMu.RLock()

	historyLen := len(s.ChatHistory)

	if historyLen == 0 {
		s.HistoryMu.RUnlock()
		return
	}

	startIndex := 0
	if historyLen > Cfg.MaxHistorySend {
		startIndex = historyLen - Cfg.MaxHistorySend
	}

	historyCopy := make([]string, historyLen-startIndex)
	copy(historyCopy, s.ChatHistory[startIndex:])

	s.HistoryMu.RUnlock()

	conn.WriteMessage(websocket.TextMessage, []byte("--- Lịch sử chat gần đây ---"))

	for _, msg := range historyCopy {
		time.Sleep(5 * time.Millisecond)
		conn.WriteMessage(websocket.TextMessage, []byte(msg))
	}

	conn.WriteMessage(websocket.TextMessage, []byte("--- Kết thúc lịch sử ---"))
}

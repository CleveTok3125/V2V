package main

import (
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gorilla/websocket"
)

func (s *ChatServer) acquireIPConnection(w http.ResponseWriter, clientIP string) bool {
	s.IpCountsMu.Lock()
	defer s.IpCountsMu.Unlock()

	if s.IpCounts[clientIP] >= Cfg.MaxConnectionsPerIP {
		log.Printf("⛔ Từ chối: %s đã vượt quá giới hạn %d kết nối.\n", clientIP, Cfg.MaxConnectionsPerIP)
		http.Error(w, "Bạn đã mở quá nhiều kết nối từ địa chỉ IP này.", http.StatusTooManyRequests)
		return false
	}
	s.IpCounts[clientIP]++
	return true
}

func (s *ChatServer) releaseIPConnection(clientIP string) {
	s.IpCountsMu.Lock()
	defer s.IpCountsMu.Unlock()

	s.IpCounts[clientIP]--
	if s.IpCounts[clientIP] <= 0 {
		delete(s.IpCounts, clientIP)
	}
}

func (s *ChatServer) registerClient(session *ClientSession, clientIP string) {
	s.ClientsMu.Lock()
	s.Clients[session.Conn] = session
	s.ClientsMu.Unlock()

	s.SendChatHistory(session.Conn)

	joinTime := time.Now().In(Cfg.Timezone)
	s.CheckAndBroadcastDate(joinTime)

	joinMsg := fmt.Sprintf("\x1b[90m%s\x1b[0m [Hệ thống]: %s đã tham gia phòng chat!", joinTime.Format("15:04"), session.DisplayName)
	log.Printf("🟢 [JOIN] %s (IP: %s)\n", session.DisplayName, clientIP)
	s.Broadcast(joinMsg, session.Conn)
}

func (s *ChatServer) unregisterClient(session *ClientSession, clientIP string) {
	session.Conn.Close()

	s.ClientsMu.Lock()
	if _, exists := s.Clients[session.Conn]; !exists {
		s.ClientsMu.Unlock()
		return
	}
	delete(s.Clients, session.Conn)
	s.ClientsMu.Unlock()

	leaveTime := time.Now().In(Cfg.Timezone)
	s.CheckAndBroadcastDate(leaveTime)

	leaveMsg := fmt.Sprintf("\x1b[90m%s\x1b[0m [Hệ thống]: %s đã rời phòng chat.", leaveTime.Format("15:04"), session.DisplayName)
	log.Printf("🔴 [LEAVE] %s (IP: %s)\n", session.DisplayName, clientIP)
	s.Broadcast(leaveMsg, nil)
}

func (s *ChatServer) KeepAlive(conn *websocket.Conn) {
	pongWait := 60 * time.Second
	pingPeriod := 50 * time.Second

	conn.SetReadDeadline(time.Now().Add(pongWait))

	conn.SetPongHandler(func(string) error {
		conn.SetReadDeadline(time.Now().Add(pongWait))
		return nil
	})

	go func() {
		ticker := time.NewTicker(pingPeriod)
		defer ticker.Stop()
		for range ticker.C {
			if err := conn.WriteControl(websocket.PingMessage, []byte{}, time.Now().Add(10*time.Second)); err != nil {
				conn.Close()
				return
			}
		}
	}()
}

func (s *ChatServer) ReadPump(session *ClientSession, clientIP string) {
	s.KeepAlive(session.Conn)

	lastMessageTime := time.Time{}

	for {
		_, msg, err := session.Conn.ReadMessage()
		if err != nil {
			break
		}

		text := string(msg)
		text = sanitizeString(text)

		if !session.Perms.CanMessageUnlimited {
			if utf8.RuneCountInString(text) > Cfg.MaxMessageLength {
				warning := fmt.Sprintf("[Hệ thống]: Tin nhắn của bạn quá dài (tối đa %d ký tự). Hãy chia nhỏ ra nhé!", Cfg.MaxMessageLength)
				session.Conn.WriteMessage(websocket.TextMessage, []byte(warning))
				continue
			}

			if strings.Count(text, "\n") > Cfg.MaxMessageLine {
				warning := "[Hệ thống]: Tin nhắn chứa quá nhiều dòng. Vui lòng gộp lại để tránh làm trôi khung chat!"
				session.Conn.WriteMessage(websocket.TextMessage, []byte(warning))
				continue
			}

			if time.Since(lastMessageTime) < Cfg.MessageCooldown {
				warning := fmt.Sprintf("[Hệ thống]: Bạn đang chat quá nhanh! Vui lòng đợi %v giữa các tin nhắn.", Cfg.MessageCooldown)
				session.Conn.WriteMessage(websocket.TextMessage, []byte(warning))
				continue
			}
		}

		lastMessageTime = time.Now()

		now := time.Now().In(Cfg.Timezone)
		s.CheckAndBroadcastDate(now)
		timeStr := now.Format("15:04")

		chatMsg := fmt.Sprintf("\x1b[90m%s\x1b[0m %s: %s", timeStr, session.DisplayName, text)
		log.Printf("💬 [MSG từ %s]: %s\n", clientIP, chatMsg)
		s.Broadcast(chatMsg, session.Conn)
	}
}

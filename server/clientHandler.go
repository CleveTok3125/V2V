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

	dynCfg := Cfg.Dynamic.Load()

	if s.IpCounts[clientIP] >= dynCfg.MaxConnectionsPerIP {
		log.Printf("⛔ Từ chối: %s đã vượt quá giới hạn %d kết nối.\n", clientIP, dynCfg.MaxConnectionsPerIP)
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

	s.SendChatHistory(session)

	joinTime := time.Now().In(Cfg.Static.Timezone)
	s.CheckAndBroadcastDate(joinTime)

	joinMsg := fmt.Sprintf("\x1b[90m%s\x1b[0m [Hệ thống]: %s đã tham gia phòng chat!", joinTime.Format("15:04"), session.DisplayName)
	log.Printf("🟢 [JOIN] %s %s (IP: %s)\n", session.DisplayName, session.Tripcode, clientIP)
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

	close(session.Send)

	leaveTime := time.Now().In(Cfg.Static.Timezone)
	s.CheckAndBroadcastDate(leaveTime)

	leaveMsg := fmt.Sprintf("\x1b[90m%s\x1b[0m [Hệ thống]: %s đã rời phòng chat.", leaveTime.Format("15:04"), session.DisplayName)
	log.Printf("🔴 [LEAVE] %s %s (IP: %s)\n", session.DisplayName, session.Tripcode, clientIP)
	s.Broadcast(leaveMsg, nil)
}

func (c *ClientSession) WritePump() {
	ticker := time.NewTicker(50 * time.Second)
	defer func() {
		ticker.Stop()
		c.Conn.Close()
	}()

	for {
		select {
		case message, ok := <-c.Send:
			c.Conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if !ok {
				c.Conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}
			c.Conn.WriteMessage(websocket.TextMessage, message)

		case <-ticker.C:
			c.Conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := c.Conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

func (s *ChatServer) ReadPump(session *ClientSession, clientIP string) {
	defer func() {
		s.unregisterClient(session, clientIP)
		session.Conn.Close()
	}()

	pongWait := 60 * time.Second
	session.Conn.SetReadLimit(int64(Cfg.Dynamic.Load().MaxMessageLength * 3))
	session.Conn.SetReadDeadline(time.Now().Add(pongWait))
	session.Conn.SetPongHandler(func(string) error {
		session.Conn.SetReadDeadline(time.Now().Add(pongWait))
		return nil
	})

	lastMessageTime := time.Time{}

	for {
		_, msg, err := session.Conn.ReadMessage()
		if err != nil {
			break
		}

		dynCfg := Cfg.Dynamic.Load()

		text := sanitizeString(string(msg))

		if strings.TrimSpace(text) == "" {
			continue
		}

		if !session.Perms.CanMessageUnlimited {
			if utf8.RuneCountInString(text) > dynCfg.MaxMessageLength {
				session.Send <- []byte(fmt.Sprintf("[Hệ thống]: Tin nhắn của bạn quá dài (tối đa %d ký tự).", dynCfg.MaxMessageLength))
				continue
			}

			if strings.Count(text, "\n") > dynCfg.MaxMessageLine {
				session.Send <- []byte("[Hệ thống]: Tin nhắn chứa quá nhiều dòng. Vui lòng gộp lại!")
				continue
			}

			if time.Since(lastMessageTime) < dynCfg.MessageCooldown {
				session.Send <- []byte(fmt.Sprintf("[Hệ thống]: Bạn đang chat quá nhanh! Vui lòng đợi %v.", dynCfg.MessageCooldown))
				continue
			}
		}

		lastMessageTime = time.Now()

		now := time.Now().In(Cfg.Static.Timezone)
		s.CheckAndBroadcastDate(now)

		tripcodeSuffix := ""
		if session.Tripcode != "" {
			tripcodeSuffix = "\n  └─ ✍️ " + session.Tripcode
		}

		newLinePrefix := " "
		if strings.Contains(text, "\n") {
			newLinePrefix = "⏎\n      "
		}

		chatMsg := fmt.Sprintf("\x1b[90m%s\x1b[0m %s:%s%s%s", now.Format("15:04"), session.DisplayName, newLinePrefix, strings.ReplaceAll(text, "\n", "\n      "), tripcodeSuffix)
		log.Printf("💬 [MSG từ %s] %s (%s): %s\n", clientIP, session.DisplayName, session.Tripcode, strings.ReplaceAll(text, "\n", "\\n"))
		s.Broadcast(chatMsg, session.Conn)
	}
}

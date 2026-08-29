package main

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"localchat/internal/filter"
	"localchat/internal/tripcolor"
	"localchat/linkify"

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

	// Release the identity slot only if this session still owns it (a newer
	// login elsewhere may have taken over the registration).
	if session.IdentityPub != "" {
		if raw, loaded := s.ActiveIdentities.Load(session.IdentityPub); loaded {
			if prev, _ := raw.(*ClientSession); prev == session {
				s.ActiveIdentities.Delete(session.IdentityPub)
			}
		}
	}

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
	lastChatActivity := time.Now()

	updateReadDeadline := func() {
		dynCfg := Cfg.Dynamic.Load()
		deadline := time.Now().Add(pongWait)

		if dynCfg.IdleChatTimeout > 0 {
			idleDeadline := lastChatActivity.Add(dynCfg.IdleChatTimeout)
			if idleDeadline.Before(deadline) {
				deadline = idleDeadline
			}
		}

		session.Conn.SetReadDeadline(deadline)
	}

	session.Conn.SetReadLimit(int64(Cfg.Dynamic.Load().MaxMessageLength * 3))
	updateReadDeadline()
	session.Conn.SetPongHandler(func(string) error {
		updateReadDeadline()
		return nil
	})

	lastMessageTime := time.Time{}

	for {
		_, msg, err := session.Conn.ReadMessage()
		if err != nil {
			if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
				dynCfg := Cfg.Dynamic.Load()
				if dynCfg.IdleChatTimeout > 0 && time.Since(lastChatActivity) >= dynCfg.IdleChatTimeout {
					log.Printf("⏱️ [IDLE TIMEOUT] %s %s (IP: %s) bị ngắt do không chat trong %v.\n", session.DisplayName, session.Tripcode, clientIP, dynCfg.IdleChatTimeout)
				}
			}
			break
		}

		dynCfg := Cfg.Dynamic.Load()

		raw := string(msg)
		if strings.TrimSpace(raw) == "" {
			updateReadDeadline()
			continue
		}
		// Try to parse as TripMessage JSON envelope for signed messages
		var tripMsg struct {
			Text string `json:"text"`
			Msg  string `json:"msg"`
			Pub  string `json:"pub"`
			Seq  uint32 `json:"seq"`
			Prev string `json:"prev"`
			Sig  string `json:"sig"`
		}
		var isTripMessage bool
		var tripMeta *TripMeta
		text := raw
		tripVerified := false
		tripBadgeColor := ""
		if err := json.Unmarshal([]byte(raw), &tripMsg); err == nil && tripMsg.Sig != "" {
			isTripMessage = true
			// Extract text
			t := tripMsg.Text
			if t == "" {
				t = tripMsg.Msg
			}
			if t == "" {
				select {
				case session.Send <- []byte("[Hệ thống]: Tin nhắn trip thiếu nội dung."):
				default:
				}
				updateReadDeadline()
				continue
			}
			text = t
			// Validate text content
			if err := filter.ValidateMessage(text); err != nil {
				select {
				case session.Send <- []byte(fmt.Sprintf("[Hệ thống]: Tin nhắn chứa ký tự không hợp lệ và đã bị từ chối (%v).", err)):
				default:
				}
				log.Printf("⛔ [FILTER REJECT] %s (%s): %v | raw=%q", session.DisplayName, clientIP, err, raw)
				updateReadDeadline()
				continue
			}
			// Trip verification
			if session.TripPub != "" && !strings.EqualFold(session.TripPub, tripMsg.Pub) {
				select {
				case session.Send <- []byte("[Hệ thống]: Pubkey trip không khớp phiên đăng nhập."):
				default:
				}
				updateReadDeadline()
				continue
			}
			pubHex := strings.ToLower(tripMsg.Pub)
			pubBytes, err1 := hex.DecodeString(pubHex)
			sigBytes, err2 := hex.DecodeString(tripMsg.Sig)
			prevBytes, err3 := hex.DecodeString(tripMsg.Prev)
			if err1 != nil || err2 != nil || err3 != nil || len(pubBytes) != ed25519.PublicKeySize || len(sigBytes) != ed25519.SignatureSize || len(prevBytes) != 32 {
				select {
				case session.Send <- []byte("[Hệ thống]: Chữ ký trip không hợp lệ."):
				default:
				}
				updateReadDeadline()
				continue
			}
			// Check seq and prev against chain
			var expectedSeq uint32 = 1
			var expectedPrev []byte = make([]byte, 32)
			if v, ok := s.TripChains.Load(pubHex); ok {
				if ch, ok := v.(TripChain); ok {
					expectedSeq = ch.Seq + 1
					if len(ch.PrevHash) == 32 {
						expectedPrev = ch.PrevHash
					}
				}
			} else if v, ok := s.TripChains.Load(strings.ToLower(pubHex)); ok {
				if ch, ok := v.(TripChain); ok {
					expectedSeq = ch.Seq + 1
					if len(ch.PrevHash) == 32 {
						expectedPrev = ch.PrevHash
					}
				}
			}
			if tripMsg.Seq != expectedSeq {
				select {
				case session.Send <- []byte(fmt.Sprintf("[Hệ thống]: Sai thứ tự trip seq %d, mong đợi %d.", tripMsg.Seq, expectedSeq)):
				default:
				}
				updateReadDeadline()
				continue
			}
			if !strings.EqualFold(tripMsg.Prev, hex.EncodeToString(expectedPrev)) {
				select {
				case session.Send <- []byte("[Hệ thống]: Chuỗi trip bị đứt (prev không khớp)."):
				default:
				}
				updateReadDeadline()
				continue
			}
			// Verify signature over canonical payload
			serverPub := ""
			if s.ServerID != nil {
				serverPub = s.ServerID.PublicKey
			}
			msgHash := sha256.Sum256([]byte(text))
			payload := tripcolor.CanonicalPayload(serverPub, tripMsg.Seq, prevBytes, msgHash[:], pubBytes)
			if !ed25519.Verify(pubBytes, payload, sigBytes) {
				select {
				case session.Send <- []byte("[Hệ thống]: Chữ ký trip không hợp lệ."):
				default:
				}
				updateReadDeadline()
				continue
			}
			// Success — update chain
			h := sha256.New()
			h.Write(prevBytes)
			h.Write(sigBytes)
			h.Write(msgHash[:])
			newPrev := h.Sum(nil)
			s.TripChains.Store(pubHex, TripChain{Seq: tripMsg.Seq, PrevHash: newPrev})
			tripVerified = true
			// Build trip meta for history
			tripMeta = &TripMeta{
				Pub:       pubHex,
				Seq:       tripMsg.Seq,
				Prev:      hex.EncodeToString(prevBytes),
				Sig:       hex.EncodeToString(sigBytes),
				ServerPub: serverPub,
				MsgHash:   hex.EncodeToString(msgHash[:]),
			}
			// Override session badge if not set
			if session.TripBadge == "" {
				h2 := sha256.Sum256(pubBytes)
				session.TripBadge = "◆ " + hex.EncodeToString(h2[:])[:8]
				session.TripPub = pubHex
				session.Tripcode = session.TripBadge
			}
			tripBadgeColor = tripcolor.BadgeColor(session.TripBadge)
		} else {
			// Non-trip message: if user has TripPub, they must sign (enforce)
			if session.TripPub != "" {
				select {
				case session.Send <- []byte("[Hệ thống]: Tin nhắn trip phải được ký."):
				default:
				}
				updateReadDeadline()
				continue
			}
			if err := filter.ValidateMessage(text); err != nil {
				select {
				case session.Send <- []byte(fmt.Sprintf("[Hệ thống]: Tin nhắn chứa ký tự không hợp lệ và đã bị từ chối (%v).", err)):
				default:
				}
				log.Printf("⛔ [FILTER REJECT] %s (%s): %v | raw=%q", session.DisplayName, clientIP, err, raw)
				updateReadDeadline()
				continue
			}
		}

		lastChatActivity = time.Now()
		updateReadDeadline()

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
			if isTripMessage {
				// Badge itself is the OSC8 hyperlink to stateless verify endpoint
				visible := session.Tripcode
				if tripVerified {
					visible = tripBadgeColor + session.Tripcode + "\x1b[0m"
				} else {
					visible = "\x1b[91m" + session.Tripcode + " ✗\x1b[0m"
				}
				if tripMeta != nil && session.Host != "" {
					// Stateless https link — works in both web and desktop OSC8 handlers
					url := fmt.Sprintf("https://%s/api/trip/verify?pub=%s&seq=%d&prev=%s&sig=%s&msg_hash=%s&server_pub=%s", session.Host, tripMeta.Pub, tripMeta.Seq, tripMeta.Prev, tripMeta.Sig, tripMeta.MsgHash, tripMeta.ServerPub)
					visible = fmt.Sprintf("\x1b]8;;%s\x1b\\%s\x1b]8;;\x1b\\", url, visible)
				}
				tripcodeSuffix = "\n  └─ ✍️ " + visible
			} else {
				tripcodeSuffix = "\n  └─ ✍️ " + session.Tripcode
			}
		}

		newLinePrefix := " "
		if strings.Contains(text, "\n") {
			newLinePrefix = "⏎\n      "
		}

		chatMsg := fmt.Sprintf("\x1b[90m%s\x1b[0m %s:%s%s%s", now.Format("15:04"), session.DisplayName, newLinePrefix, strings.ReplaceAll(linkify.Linkify(text), "\n", "\n      "), tripcodeSuffix)
		log.Printf("💬 [MSG từ %s] %s (%s): %s\n", clientIP, session.DisplayName, session.Tripcode, strings.ReplaceAll(text, "\n", "\\n"))
		if tripMeta != nil {
			s.BroadcastWithTrip(chatMsg, tripMeta, session.Conn)
		} else {
			s.Broadcast(chatMsg, session.Conn)
		}
	}
}

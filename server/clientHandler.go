package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/CleveTok3125/V2V/internal/filter"
	"github.com/CleveTok3125/V2V/internal/guard"
	"github.com/CleveTok3125/V2V/internal/trip"
	"github.com/CleveTok3125/V2V/internal/trustedproxy"

	"github.com/gorilla/websocket"
)

// contentLoggingEnabled reports whether message content may be logged.
// NO_CONTENT_LOGS turns it off; operational metadata is unaffected.
func contentLoggingEnabled() bool {
	return !Cfg.Static.NoContentLogs
}

// readLimitFor converts the configured max message length to the socket
// read limit. The cast happens before the multiply so a large value
// cannot overflow int, and a negative result (which gorilla treats as
// unlimited) is clamped to zero.
func readLimitFor(maxMessageLength int) int64 {
	limit := int64(maxMessageLength) * 3
	if limit < 0 {
		return 0
	}
	return limit
}

// logFilterReject records a rejected message. The validator error and the
// sender identity stay; the raw payload is only included when content
// logging is enabled (NO_CONTENT_LOGS=false).
func logFilterReject(session *ClientSession, clientIP string, err error, raw string) {
	if !contentLoggingEnabled() {
		logWarnf("⛔ [FILTER REJECT] %s (%s): %v", session.DisplayName, clientIP, err)
		return
	}
	logWarnf("⛔ [FILTER REJECT] %s (%s): %v | raw=%q", session.DisplayName, clientIP, err, trustedproxy.Clip(raw, 512))
}

func (s *ChatServer) acquireIPConnection(w http.ResponseWriter, clientIP string) bool {
	s.IpCountsMu.Lock()
	defer s.IpCountsMu.Unlock()

	dynCfg := Cfg.Dynamic.Load()

	if s.IpCounts[clientIP] >= dynCfg.MaxConnectionsPerIP {
		logWarnf("⛔ Từ chối: %s đã vượt quá giới hạn %d kết nối.\n", clientIP, dynCfg.MaxConnectionsPerIP)
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

// allowHistorySegment enforces the HistorySegmentCooldown knob: true
// stamps the session and allows the request. Deliberately separate
// from MessageCooldown so tuning chat never retunes history paging.
// Only ReadPump calls it.
func (s *ChatServer) allowHistorySegment(session *ClientSession, now time.Time) bool {
	if cd := Cfg.Dynamic.Load().HistorySegmentCooldown; !session.LastSegmentTime.IsZero() && now.Sub(session.LastSegmentTime) < cd {
		return false
	}
	session.LastSegmentTime = now
	return true
}

func (h *Hub) registerClient(session *ClientSession, clientIP string) {
	h.ClientsMu.Lock()
	h.Clients[session.Conn] = session
	h.ClientsMu.Unlock()

	// Track the live holder of each privileged identity so a parallel
	// login elsewhere triggers the concurrent-use alert. Newest wins;
	// unregisterClient releases only if it still owns the slot.
	if session.IdentityPub != "" {
		if prev, loaded := h.ActiveIdentities.LoadOrStore(session.IdentityPub, session); loaded {
			if old, _ := prev.(*ClientSession); old != nil && old != session {
				h.alertConcurrentIdentity(session.IdentityPub, clientIP)
				h.ActiveIdentities.Store(session.IdentityPub, session)
			}
		}
	}

	// Hold BroadcastMu across replay + own-join: no live chat may
	// interleave between replay lines and the trailer, otherwise the
	// client collects foreign hashes into its fork window and jumps
	// tips mid-sync. Notices (no BroadcastMu) can still slip in, but
	// they carry no chain fields and never disturb tip accounting.
	// Lock order stays BroadcastMu -> LastMessageDateMu -> HistoryMu
	// (Chain.Mu) -> ClientsMu: the Clients map insert in this function
	// is sequential, never nested.
	h.BroadcastMu.Lock()
	h.chain.SendChatHistory(session)

	joinTime := time.Now().In(Cfg.Static.Timezone)
	h.CheckAndBroadcastDate(joinTime)

	joinMsg := fmt.Sprintf("\x1b[90m%s\x1b[0m [Hệ thống]: %s đã tham gia phòng chat!", joinTime.Format("15:04"), session.DisplayName)
	logInfof("🟢 [JOIN] %s %s (IP: %s)\n", session.DisplayName, session.Tripcode, clientIP)
	// The joiner receives its own join too (nil sender): chain continuity
	// requires every client to see every link; display gating (!showJoin)
	// still hides it locally.
	h.BroadcastNotice(joinMsg, "join", nil)
	h.BroadcastMu.Unlock()
}

func (h *Hub) unregisterClient(session *ClientSession, clientIP string) {
	if session.Conn != nil {
		session.Conn.Close()
	}

	h.ClientsMu.Lock()
	if _, exists := h.Clients[session.Conn]; !exists {
		h.ClientsMu.Unlock()
		return
	}
	delete(h.Clients, session.Conn)
	h.ClientsMu.Unlock()

	// Release the identity slot only if this session still owns it (a newer
	// login elsewhere may have taken over the registration).
	if session.IdentityPub != "" {
		if raw, loaded := h.ActiveIdentities.Load(session.IdentityPub); loaded {
			if prev, _ := raw.(*ClientSession); prev == session {
				h.ActiveIdentities.Delete(session.IdentityPub)
			}
		}
	}

	// Release display name serial slot
	h.releaseDisplayName(session.DisplayName)

	close(session.Send)

	leaveTime := time.Now().In(Cfg.Static.Timezone)
	h.CheckAndBroadcastDate(leaveTime)

	leaveMsg := fmt.Sprintf("\x1b[90m%s\x1b[0m [Hệ thống]: %s đã rời phòng chat.", leaveTime.Format("15:04"), session.DisplayName)
	logInfof("🔴 [LEAVE] %s %s (IP: %s)\n", session.DisplayName, session.Tripcode, clientIP)
	h.BroadcastNotice(leaveMsg, "leave", nil)
}

// releaseDisplayName frees the serial slot held by a generated display
// name. Safe for a name that was never registered.
func (h *Hub) releaseDisplayName(name string) {
	h.DisplayNameCountMu.Lock()
	delete(h.DisplayNameCount, name)
	h.DisplayNameCountMu.Unlock()
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
			// A dead/slow peer must end the pump, not wedge it while
			// ReadPump keeps the slot pinned.
			if err := c.Conn.SetWriteDeadline(time.Now().Add(10 * time.Second)); err != nil {
				return
			}
			if !ok {
				c.Conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}
			if err := c.Conn.WriteMessage(websocket.TextMessage, message); err != nil {
				return
			}

		case <-ticker.C:
			if err := c.Conn.SetWriteDeadline(time.Now().Add(10 * time.Second)); err != nil {
				return
			}
			if err := c.Conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

func (s *ChatServer) ReadPump(session *ClientSession, clientIP string) {
	defer func() {
		s.Hub.unregisterClient(session, clientIP)
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

	session.Conn.SetReadLimit(readLimitFor(Cfg.Dynamic.Load().MaxMessageLength))
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
					logInfof("⏱️ [IDLE TIMEOUT] %s %s (IP: %s) bị ngắt do không chat trong %v.\n", session.DisplayName, session.Tripcode, clientIP, dynCfg.IdleChatTimeout)
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
		// On-demand older segment (paged history): served in replay
		// format, never chained, never counted as chat. A chat
		// envelope never carries "type", so this cannot misfire on
		// chat; a forged request only fetches the requester's own
		// history window.
		var histReq HistoryRequest
		if err := json.Unmarshal([]byte(raw), &histReq); err == nil && histReq.Type == "history_request" {
			limit := histReq.Limit
			if limit <= 0 || limit > dynCfg.MaxHistorySend {
				limit = dynCfg.MaxHistorySend
			}
			if !s.allowHistorySegment(session, time.Now()) {
				select {
				case session.Send <- []byte("[Hệ thống]: Yêu cầu lịch sử cũ quá nhanh, thử lại sau."):
				default:
				}
				updateReadDeadline()
				continue
			}
			s.serveHistorySegment(session, histReq.Before, limit)
			updateReadDeadline()
			continue
		}
		// Break: every chat message arrives in a JSON envelope carrying
		// the sender's per-session counter. The server relays tmp_id
		// verbatim into the wire message but never assigns or alters it.
		var tripMsg struct {
			Text        string `json:"text"`
			Msg         string `json:"msg"`
			Pub         string `json:"pub"`
			Seq         uint32 `json:"seq"`
			Prev        string `json:"prev"`
			Sig         string `json:"sig"`
			DisplayName string `json:"display_name"`
			TmpID       uint64 `json:"tmp_id"`
			ReplyTo     uint64 `json:"reply_to"`
		}
		var tripMeta *TripMeta
		var msgTmpID uint64
		var msgReplyTo uint64
		text := raw
		if err := json.Unmarshal([]byte(raw), &tripMsg); err == nil && (tripMsg.Sig != "" || tripMsg.TmpID != 0 || strings.TrimSpace(tripMsg.Text+tripMsg.Msg) != "") {
			if tripMsg.TmpID == 0 {
				select {
				case session.Send <- []byte("[Hệ thống]: Tin nhắn thiếu ID phiên (tmp_id). Hãy update client bản mới."):
				default:
				}
				updateReadDeadline()
				continue
			}
			msgTmpID = tripMsg.TmpID
			msgReplyTo = tripMsg.ReplyTo
			// Cheap sanity: quotes must name a message that exists
			// (heights start at 1, so anything above the tip is future).
			s.Chain.Mu.RLock()
			tipReady, tipHeight := s.Chain.ready, s.Chain.height
			s.Chain.Mu.RUnlock()
			if msgReplyTo != 0 && tipReady && msgReplyTo > tipHeight {
				select {
				case session.Send <- []byte("[Hệ thống]: Tin reply dẫn tới ID chưa tồn tại."):
				default:
				}
				updateReadDeadline()
				continue
			}
			if tripMsg.Sig == "" {
				// Unsigned envelope: plain chat text with a session counter.
				t := tripMsg.Text
				if t == "" {
					t = tripMsg.Msg
				}
				if t == "" {
					select {
					case session.Send <- []byte("[Hệ thống]: Tin nhắn trống."):
					default:
					}
					updateReadDeadline()
					continue
				}
				text = t
				if err := filter.ValidateMessage(text); err != nil {
					select {
					case session.Send <- []byte(fmt.Sprintf("[Hệ thống]: Tin nhắn chứa ký tự không hợp lệ và đã bị từ chối (%v).", err)):
					default:
					}
					logFilterReject(session, clientIP, err, raw)
					updateReadDeadline()
					continue
				}
			} else {
				// Signed envelope below (sets text after verification).
				text = ""
			}
		} else {
			select {
			case session.Send <- []byte("[Hệ thống]: Định dạng tin nhắn cũ không còn hỗ trợ. Hãy update client bản mới."):
			default:
			}
			updateReadDeadline()
			continue
		}
		if err := json.Unmarshal([]byte(raw), &tripMsg); err == nil && tripMsg.Sig != "" {
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
				logFilterReject(session, clientIP, err, raw)
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
			// Quick hex length check before heavy verify
			if len(pubHex) != 64 || len(tripMsg.Sig) != 128 || len(tripMsg.Prev) != 64 {
				select {
				case session.Send <- []byte("[Hệ thống]: Chữ ký trip không hợp lệ."):
				default:
				}
				updateReadDeadline()
				continue
			}
			// Check seq and prev against chain — locked per-pub to prevent TOCTOU fork
			s.TripChainsMu.Lock()
			var expectedSeq uint32 = 1
			var expectedPrev []byte = make([]byte, 32)
			if v, ok := s.TripChains.Load(pubHex); ok {
				if ch, ok := v.(TripChain); ok {
					expectedSeq = ch.Seq + 1
					if len(ch.PrevHash) == 32 {
						expectedPrev = ch.PrevHash
					}
				}
			}
			if tripMsg.Seq != expectedSeq {
				s.TripChainsMu.Unlock()
				select {
				case session.Send <- []byte(fmt.Sprintf("[Hệ thống]: Sai thứ tự trip seq %d, mong đợi %d.", tripMsg.Seq, expectedSeq)):
				default:
				}
				updateReadDeadline()
				continue
			}
			if !strings.EqualFold(tripMsg.Prev, hex.EncodeToString(expectedPrev)) {
				s.TripChainsMu.Unlock()
				select {
				case session.Send <- []byte("[Hệ thống]: Chuỗi trip bị đứt (prev không khớp)."):
				default:
				}
				updateReadDeadline()
				continue
			}
			// Verify signature via shared trip package (checks msg_hash + displayName binding)
			serverPub := ""
			if s.ServerID != nil {
				serverPub = strings.ToLower(s.ServerID.PublicKey)
			}
			msgHash := sha256.Sum256([]byte(text))
			msgHashHex := hex.EncodeToString(msgHash[:])
			res, err := trip.Verify(trip.VerifyParams{
				Text:        text,
				DisplayName: session.DisplayName,
				ServerPub:   serverPub,
				PubHex:      pubHex,
				Seq:         tripMsg.Seq,
				PrevHex:     tripMsg.Prev,
				SigHex:      tripMsg.Sig,
				MsgHashHex:  msgHashHex,
				TmpID:       tripMsg.TmpID,
				ReplyTo:     tripMsg.ReplyTo,
			})
			if err != nil {
				s.TripChainsMu.Unlock()
				select {
				case session.Send <- []byte("[Hệ thống]: Chữ ký trip không hợp lệ."):
				default:
				}
				updateReadDeadline()
				continue
			}
			// Success — update chain via helper in trip package result
			s.TripChains.Store(pubHex, TripChain{Seq: tripMsg.Seq, PrevHash: res.NewPrev, LastSeen: time.Now()})
			s.TripChainsMu.Unlock()
			// Build trip meta for history — store displayName as well for verification
			// Use res fields (already hex) but keep consistent with verified data
			tripMeta = &TripMeta{
				Pub:         res.PubHex,
				Seq:         res.Seq,
				Prev:        res.PrevHex,
				Sig:         res.SigHex,
				ServerPub:   res.ServerPub,
				MsgHash:     res.MsgHash,
				DisplayName: session.DisplayName,
				TmpID:       tripMsg.TmpID,
				ReplyTo:     tripMsg.ReplyTo,
			}
			// Override session badge if not set
			if session.TripBadge == "" {
				session.TripBadge = res.Badge
				session.TripPub = res.PubHex
				session.Tripcode = res.Badge
			}
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
				logFilterReject(session, clientIP, err, raw)
				updateReadDeadline()
				continue
			}
		}

		lastChatActivity = time.Now()
		updateReadDeadline()

		if err := guard.ValidateMessageForSend(text, lastMessageTime, &guard.Limits{
			MaxMessageLength: dynCfg.MaxMessageLength,
			MaxMessageLine:   dynCfg.MaxMessageLine,
			MessageCooldown:  dynCfg.MessageCooldown,
		}, session.Perms.CanMessageUnlimited); err != nil {
			// Unicast warnings must never block ReadPump: if WritePump is
			// wedged and Send is full, drop the warning instead of leaking
			// the goroutine and pinning the IP slot.
			warn := func(msg string) {
				select {
				case session.Send <- []byte(msg):
				default:
				}
			}
			switch err {
			case guard.ErrTooLong:
				warn(fmt.Sprintf("[Hệ thống]: Tin nhắn của bạn quá dài (tối đa %d ký tự).", dynCfg.MaxMessageLength))
			case guard.ErrTooManyLines:
				warn("[Hệ thống]: Tin nhắn chứa quá nhiều dòng. Vui lòng gộp lại!")
			case guard.ErrTooFast:
				warn(fmt.Sprintf("[Hệ thống]: Bạn đang chat quá nhanh! Vui lòng đợi %v.", dynCfg.MessageCooldown))
			default:
				warn(fmt.Sprintf("[Hệ thống]: Tin nhắn chứa ký tự không hợp lệ và đã bị từ chối (%v).", err))
			}
			continue
		}

		lastMessageTime = time.Now()

		now := time.Now().In(Cfg.Static.Timezone)
		s.Hub.CheckAndBroadcastDate(now)

		wire := WireMessage{
			Type:        "chat",
			Time:        now.Format("15:04"),
			DisplayName: session.DisplayName,
			Text:        text,
			Trip:        tripMeta,
			TmpID:       msgTmpID,
			ReplyTo:     msgReplyTo,
		}
		s.Hub.BroadcastWire(wire, session.Conn, s.serverPub())
	}
}

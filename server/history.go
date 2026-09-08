package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"

	"github.com/CleveTok3125/V2V/internal/strutil"
	"time"

	"github.com/CleveTok3125/V2V/internal/filter"
	"github.com/CleveTok3125/V2V/internal/trip"

	"github.com/gorilla/websocket"
)

func (s *ChatServer) appendMessageToHistory(msg string) {
	s.HistoryMu.Lock()
	defer s.HistoryMu.Unlock()
	s.appendMessageLocked(msg)
}

// appendMessageLocked appends one history line with dual-limit eviction.
// Caller must hold HistoryMu.
func (s *ChatServer) appendMessageLocked(msg string) {
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
			displayName := wireForVerify.DisplayName
			if displayName == "" {
				displayName = tripForChain.DisplayName
			}
			serverPub := tripForChain.ServerPub
			if serverPub == "" && s.ServerID != nil {
				serverPub = s.ServerID.PublicKey
			}
			// For wire case, set Text field so trip.Verify recomputes correctly
			verifyText := wireForVerify.Text
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
				ReplyTo:     tripForChain.ReplyTo,
			})
			if err != nil {
				log.Printf("⚠️ [HISTORY TAMPER] %s seq %d: %v", strutil.Short(tripForChain.Pub), tripForChain.Seq, err)
				continue
			}
			// Success: derive newPrev via result. Malformed hex aborts
			// the record instead of chaining zeros.
			prevBytes, err := hex.DecodeString(tripForChain.Prev)
			if err != nil {
				log.Printf("⚠️ [HISTORY TAMPER] %s: bad prev hex: %v", strutil.Short(tripForChain.Pub), err)
				continue
			}
			sigBytes, err := hex.DecodeString(tripForChain.Sig)
			if err != nil {
				log.Printf("⚠️ [HISTORY TAMPER] %s: bad sig hex: %v", strutil.Short(tripForChain.Pub), err)
				continue
			}
			hashBytes, err := hex.DecodeString(tripForChain.MsgHash)
			if err != nil {
				log.Printf("⚠️ [HISTORY TAMPER] %s: bad msg_hash hex: %v", strutil.Short(tripForChain.Pub), err)
				continue
			}
			h := sha256.New()
			h.Write(prevBytes)
			h.Write(sigBytes)
			h.Write(hashBytes)
			newPrev := h.Sum(nil)
			// An older duplicate later in the file must not rewind a
			// newer tip: keep the highest sequence per key.
			if cur, ok := s.TripChains.Load(tripForChain.Pub); !ok || tripForChain.Seq > cur.(TripChain).Seq {
				s.TripChains.Store(tripForChain.Pub, TripChain{Seq: tripForChain.Seq, PrevHash: newPrev})
			}
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

// BroadcastNotice sends a server-originated notification (join/leave/date)
// that never enters the hash chain: it carries no authorship or ordering
// evidence, only display text. Stored for replay, broadcast live.
// Management lines that must serve as evidence use BroadcastAudit;
// unicast warnings stay raw strings (per-client, never chained).
func (s *ChatServer) BroadcastNotice(text, kind string, sender *websocket.Conn) {
	now := time.Now().In(Cfg.Static.Timezone)
	wire := WireMessage{Type: "system", Time: now.Format("15:04"), SysKind: kind, Text: text}
	data, _ := json.Marshal(wire)
	s.appendMessageToHistory(string(data))
	if s.HistoryStore != nil {
		s.HistoryStore.EnqueueWire(wire, now)
	}

	s.ClientsMu.RLock()
	defer s.ClientsMu.RUnlock()

	for conn, client := range s.Clients {
		if conn != sender {
			s.sendWithRetry(conn, client, data, true)
		}
	}
}

// BroadcastAudit chains a server-originated management line as evidence:
// unlike notices, audit lines occupy chain positions and verify like
// chat. No producers yet; the route exists so management evidence never
// rides the notice path by mistake.
func (s *ChatServer) BroadcastAudit(text string, sender *websocket.Conn) {
	now := time.Now().In(Cfg.Static.Timezone)
	s.BroadcastMu.Lock()
	defer s.BroadcastMu.Unlock()
	wire := s.linkAndStore(WireMessage{Type: "system", Time: now.Format("15:04"), SysKind: "audit", Text: text})
	data, _ := json.Marshal(wire)

	s.ClientsMu.RLock()
	defer s.ClientsMu.RUnlock()

	for conn, client := range s.Clients {
		if conn != sender {
			s.sendWithRetry(conn, client, data, true)
		}
	}
}

func (s *ChatServer) BroadcastWire(wire WireMessage, sender *websocket.Conn) {
	s.BroadcastMu.Lock()
	defer s.BroadcastMu.Unlock()
	wire = s.linkAndStore(wire)
	data, _ := json.Marshal(wire)
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

func (s *ChatServer) CheckAndBroadcastDate(now time.Time) {
	currentDate := now.Format("02/01/2006")

	s.LastMessageDateMu.Lock()
	defer s.LastMessageDateMu.Unlock()

	if s.LastMessageDate == "" || s.LastMessageDate != currentDate {
		s.LastMessageDate = currentDate

		dateMsg := fmt.Sprintf("\x1b[36m--- Ngày %s ---\x1b[0m", currentDate)

		s.BroadcastNotice(dateMsg, "date", nil)
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

	// Replay filters join/leave unless the session asked for them.
	// Dates, audits and untagged lines always go. Filtered lines never
	// occupied chain positions, so the replayed window has no gaps.
	var minHeight, maxHeight uint64
	var haveHeight bool
	sent := 0

	session.Send <- []byte("--- Lịch sử chat gần đây ---")
	for _, msgStr := range historyCopy {
		// Keep history messages as stored (could be legacy ANSI string or WireMessage JSON)
		// For WireMessage JSON, send as is; for legacy, clean and send
		var wire WireMessage
		wireErr := json.Unmarshal([]byte(msgStr), &wire)
		if wireErr == nil && wire.Type == "system" &&
			(wire.SysKind == "join" || wire.SysKind == "leave") && !session.WantJoins {
			continue
		}
		// Window bounds cover sent chained lines only: skipped lines
		// must not widen the range, and unchained notices (height 0)
		// must not drag the minimum to zero.
		if wireErr == nil && wire.ChainHeight > 0 {
			if !haveHeight || wire.ChainHeight < minHeight {
				minHeight = wire.ChainHeight
			}
			if !haveHeight || wire.ChainHeight > maxHeight {
				maxHeight = wire.ChainHeight
			}
			haveHeight = true
		}
		if wireErr == nil {
			if wire.Type == "chat" {
				session.Send <- []byte(msgStr)
			} else {
				cleaned := filter.CleanHistoryMessage(msgStr)
				session.Send <- []byte(cleaned)
			}
		} else {
			cleaned := filter.CleanHistoryMessage(msgStr)
			session.Send <- []byte(cleaned)
		}
		sent++
	}
	session.Send <- []byte(fmt.Sprintf("--- Kết thúc lịch sử (%d/%d) ---", sent, len(historyCopy)))
	trailer, _ := json.Marshal(HistorySync{Type: "history_sync", MinHeight: minHeight, MaxHeight: maxHeight,
		Sent: sent, Total: len(historyCopy)})
	session.Send <- trailer
}

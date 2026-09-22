package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/CleveTok3125/V2V/internal/strutil"
	"time"

	"github.com/CleveTok3125/V2V/internal/filter"
	"github.com/CleveTok3125/V2V/internal/trip"

	"github.com/gorilla/websocket"
)

func (c *ChainService) appendMessageToHistory(msg string) {
	c.Mu.Lock()
	defer c.Mu.Unlock()
	c.appendMessageLocked(msg)
}

// appendMessageLocked appends one history line with dual-limit eviction.
// Caller must hold HistoryMu (Chain.Mu).
func (c *ChainService) appendMessageLocked(msg string) {
	msgSize := len(msg)
	c.History = append(c.History, msg)
	c.HistorySize += msgSize

	for c.HistorySize > Cfg.Dynamic.Load().MaxHistoryBytes && len(c.History) > 0 {
		oldestSize := len(c.History[0])
		c.HistorySize -= oldestSize

		c.History[0] = ""
		c.History = c.History[1:]
	}
	// Shrink underlying array when cap bloats >4*len to avoid holding 20MiB when only 5MiB needed
	if cap(c.History) > 4*len(c.History) && cap(c.History) > 1024 {
		newCap := len(c.History)
		if newCap < 1024 {
			newCap = 1024
		}
		n := make([]string, len(c.History), newCap)
		copy(n, c.History)
		c.History = n
	}
}

func (s *ChatServer) InitHistoryStore(path string, maxSizeMB int) error {
	store, err := NewHistoryStore(path, maxSizeMB)
	if err != nil {
		return err
	}

	s.Chain.Store = store

	if store == nil {
		return nil
	}

	records, err := store.LoadRecords()
	if err != nil {
		return fmt.Errorf("không thể nạp history từ disk: %w", err)
	}

	for _, rec := range records {
		var tripForChain *TripMeta
		var wireForVerify *WireMessage
		msgForHistory, ok := recordLine(rec)
		if !ok {
			continue
		}
		if rec.Wire != nil {
			tripForChain = rec.Wire.Trip
			wireForVerify = rec.Wire
		}
		s.Chain.appendMessageToHistory(msgForHistory)
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
				logWarnf("⚠️ [HISTORY TAMPER] %s seq %d: %v", strutil.Short(tripForChain.Pub), tripForChain.Seq, err)
				continue
			}
			// Success: derive newPrev via result. Malformed hex aborts
			// the record instead of chaining zeros.
			prevBytes, err := hex.DecodeString(tripForChain.Prev)
			if err != nil {
				logWarnf("⚠️ [HISTORY TAMPER] %s: bad prev hex: %v", strutil.Short(tripForChain.Pub), err)
				continue
			}
			sigBytes, err := hex.DecodeString(tripForChain.Sig)
			if err != nil {
				logWarnf("⚠️ [HISTORY TAMPER] %s: bad sig hex: %v", strutil.Short(tripForChain.Pub), err)
				continue
			}
			hashBytes, err := hex.DecodeString(tripForChain.MsgHash)
			if err != nil {
				logWarnf("⚠️ [HISTORY TAMPER] %s: bad msg_hash hex: %v", strutil.Short(tripForChain.Pub), err)
				continue
			}
			h := sha256.New()
			h.Write(prevBytes)
			h.Write(sigBytes)
			h.Write(hashBytes)
			newPrev := h.Sum(nil)
			// An older duplicate later in the file must not rewind a
			// newer tip: keep the highest sequence per key.
			if cur, ok := s.TripChains.Load(tripForChain.Pub); !ok {
				s.TripChains.Store(tripForChain.Pub, TripChain{Seq: tripForChain.Seq, PrevHash: newPrev, LastSeen: time.Now()})
			} else if ch, ok := cur.(TripChain); ok && tripForChain.Seq > ch.Seq {
				s.TripChains.Store(tripForChain.Pub, TripChain{Seq: tripForChain.Seq, PrevHash: newPrev, LastSeen: time.Now()})
			}
		}
	}

	loggedCount := len(s.Chain.History)
	if loggedCount > 0 {
		logInfof("📚 Đã phục hồi %d tin nhắn history từ disk", loggedCount)
	}

	return nil
}

func (h *Hub) sendWithRetry(conn *websocket.Conn, client *ClientSession, msg []byte, isSystem bool) {
	// System/date messages get one bounded retry to avoid drift when
	// burst follows: wait up to 20ms for buffer space instead of
	// sleeping blindly, so an early drain delivers immediately.
	select {
	case client.Send <- msg:
	default:
		if isSystem {
			select {
			case client.Send <- msg:
			case <-time.After(20 * time.Millisecond):
			}
		}
	}
}

// BroadcastNotice sends a server-originated notification (join/leave/date)
// that never enters the hash chain: it carries no authorship or ordering
// evidence, only display text. Stored for replay, broadcast live.
// Management lines that must serve as evidence use BroadcastAudit;
// unicast warnings stay raw strings (per-client, never chained).
// fanout sends one marshaled wire to every client except sender.
// isSystem selects the retry policy; echo returns a delivery
// confirmation to the sender itself. Callers hold BroadcastMu when
// ordering against chained records matters; the notice path skips it
// (registerClient already holds it across replay+join, so taking it
// here would self-deadlock). Callers must NOT hold ClientsMu.
func (h *Hub) fanout(data []byte, sender *websocket.Conn, isSystem, echo bool) {
	h.ClientsMu.RLock()
	defer h.ClientsMu.RUnlock()

	for conn, client := range h.Clients {
		if conn != sender {
			h.sendWithRetry(conn, client, data, isSystem)
		}
	}
	if echo && sender != nil {
		if sess, ok := h.Clients[sender]; ok {
			select {
			case sess.Send <- data:
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
func (h *Hub) BroadcastNotice(text, kind string, sender *websocket.Conn) {
	now := time.Now().In(Cfg.Static.Timezone)
	wire := WireMessage{Type: "system", Time: now.Format("15:04"), SysKind: kind, Text: text}
	data, _ := json.Marshal(wire)
	h.chain.appendMessageToHistory(string(data))
	if h.chain.Store != nil {
		h.chain.Store.EnqueueWire(wire, now)
	}
	h.fanout(data, sender, true, false)
}

// BroadcastAudit chains a server-originated management line as evidence:
// unlike notices, audit lines occupy chain positions and verify like
// chat. No producers yet; the route exists so management evidence never
// rides the notice path by mistake.
func (h *Hub) BroadcastAudit(text string, sender *websocket.Conn, serverPub string) {
	now := time.Now().In(Cfg.Static.Timezone)
	h.BroadcastMu.Lock()
	defer h.BroadcastMu.Unlock()
	wire := h.chain.linkAndStore(WireMessage{Type: "system", Time: now.Format("15:04"), SysKind: "audit", Text: text}, serverPub)
	data, _ := json.Marshal(wire)

	h.fanout(data, sender, true, false)
}

func (h *Hub) BroadcastWire(wire WireMessage, sender *websocket.Conn, serverPub string) {
	h.BroadcastMu.Lock()
	defer h.BroadcastMu.Unlock()
	wire = h.chain.linkAndStore(wire, serverPub)
	data, _ := json.Marshal(wire)
	// Echo to the sender doubles as delivery confirmation so it can
	// replace its grey placeholder with the confirmed rendering.
	h.fanout(data, sender, false, true)
}

// serveHistorySegment serves one on-demand older segment under the
// broadcast lock, so live chats cannot interleave between segment
// lines and the trailer (same ordering contract as the join replay:
// BroadcastMu -> Chain.Mu, never the reverse).
func (s *ChatServer) serveHistorySegment(session *ClientSession, before uint64, limit int) {
	s.Hub.BroadcastMu.Lock()
	defer s.Hub.BroadcastMu.Unlock()
	s.Chain.SendChatSegment(session, before, limit)
}

func (h *Hub) CheckAndBroadcastDate(now time.Time) {
	currentDate := now.Format("02/01/2006")

	h.LastMessageDateMu.Lock()
	defer h.LastMessageDateMu.Unlock()

	if h.LastMessageDate == "" || h.LastMessageDate != currentDate {
		h.LastMessageDate = currentDate

		dateMsg := fmt.Sprintf("\x1b[36m--- Ngày %s ---\x1b[0m", currentDate)

		h.BroadcastNotice(dateMsg, "date", nil)
	}
}

func (c *ChainService) SendChatHistory(session *ClientSession) {
	c.Mu.RLock()

	historyLen := len(c.History)

	if historyLen == 0 {
		c.Mu.RUnlock()
		return
	}

	dynCfg := Cfg.Dynamic.Load()

	startIndex := 0
	if historyLen > dynCfg.MaxHistorySend {
		startIndex = historyLen - dynCfg.MaxHistorySend
	}

	historyCopy := make([]string, historyLen-startIndex)
	copy(historyCopy, c.History[startIndex:])
	c.Mu.RUnlock()

	c.sendReplay(session, historyCopy, "--- Lịch sử chat gần đây ---", false)
}

// windowRing keeps the last n fed lines oldest→newest: a full forward
// stream collapses to its tail window with O(limit) memory and O(1)
// amortized feed (circular overwrite, no memmove churn on big scans).
type windowRing struct {
	buf   []string
	start int
	n     int
	limit int
	done  bool
}

func newWindowRing(limit int) *windowRing {
	if limit < 0 {
		limit = 0
	}
	return &windowRing{limit: limit}
}

// feed adds one stored line and reports whether the global cutoff is
// reached: the first chained height at or above before (before 0 means
// the tail window, never cut). The cutoff line itself is excluded.
func (r *windowRing) feed(msgStr string, before uint64) bool {
	if r.done {
		return true
	}
	if before != 0 {
		var wire WireMessage
		if err := json.Unmarshal([]byte(msgStr), &wire); err == nil && wire.ChainHeight != 0 && wire.ChainHeight >= before {
			r.done = true
			return true
		}
	}
	if r.limit <= 0 {
		return false
	}
	if r.n < r.limit {
		r.buf = append(r.buf, msgStr)
		r.n++
	} else {
		r.buf[r.start] = msgStr
		r.start = (r.start + 1) % r.limit
	}
	return false
}

// lines drains the window oldest→newest.
func (r *windowRing) lines() []string {
	out := make([]string, 0, r.n)
	for i := 0; i < r.n; i++ {
		out = append(out, r.buf[(r.start+i)%r.limit])
	}
	return out
}

// selectWindow returns up to limit stored lines before the cutoff (see
// feed). Unchained lines travel by position; an exhausted window is an
// empty slice (the caller still terminates the stream).
func selectWindow(lines []string, before uint64, limit int) []string {
	if limit <= 0 {
		return nil
	}
	r := newWindowRing(limit)
	for _, msgStr := range lines {
		if r.feed(msgStr, before) {
			break
		}
	}
	return r.lines()
}

// recordLine renders one disk record in RAM string form (marshaled wire
// or raw message), mirroring InitHistoryStore. False means the record
// holds nothing renderable and must be skipped like the loader skips it.
func recordLine(rec historyRecord) (string, bool) {
	if rec.Wire != nil {
		data, _ := json.Marshal(rec.Wire)
		return string(data), true
	}
	if rec.Message != "" {
		return rec.Message, true
	}
	return "", false
}

// oldestChained returns the smallest chained height in stored lines, or
// 0 when none exists. RAM holds a height-suffix of the log, so disk
// generations only need lines below this bound: chained content
// partitions exactly, with no overlap and no gap.
func oldestChained(lines []string) uint64 {
	var oldest uint64
	found := false
	for _, msgStr := range lines {
		var wire WireMessage
		if err := json.Unmarshal([]byte(msgStr), &wire); err != nil || wire.ChainHeight == 0 {
			continue
		}
		if !found || wire.ChainHeight < oldest {
			oldest, found = wire.ChainHeight, true
		}
	}
	if !found {
		return 0
	}
	return oldest
}

// SendChatSegment sends up to limit stored lines older than before in
// replay format. before is an exclusive chain height; 0 means the tail
// window over the whole history. Unchained lines carry no height, so
// they travel with their neighbors by position: the cutoff is the
// first line at or above before. An exhausted window still terminates
// the stream (header, plain exhausted footer, trailer) so the
// requester never hangs waiting. Lines evicted from RAM are served
// from disk when the lookup level allows (see windowBefore); the RAM
// seam may repeat a few unchained lines, chained content stays exact.
func (c *ChainService) SendChatSegment(session *ClientSession, before uint64, limit int) {
	if limit <= 0 {
		return
	}
	ring := newWindowRing(limit)
	if before != 0 && c.Store != nil {
		if level := Cfg.Dynamic.Load().HistoryDiskLookup; level > DiskLookupOff {
			c.Mu.RLock()
			bound := before
			if oldest := oldestChained(c.History); oldest != 0 && oldest < bound {
				bound = oldest
			}
			store := c.Store
			c.Mu.RUnlock()
			store.windowBefore(ring, bound, level)
		}
	}
	if !ring.done {
		c.Mu.RLock()
		for _, msgStr := range c.History {
			if ring.feed(msgStr, before) {
				break
			}
		}
		c.Mu.RUnlock()
	}

	c.sendReplay(session, ring.lines(), "--- Lịch sử cũ ---", true)
}

// sendReplay renders stored lines in replay format: header, content,
// human footer with counts, and a HistorySync trailer. lines must be a
// private copy. Sends never block (drops on a full peer buffer are
// counted and logged); the trailer reports what was actually queued.
// A segment footer is labeled as older history, and says plainly when
// the window holds no chained line (exhausted) instead of a bare zero
// count. Both footer variants keep the boundary substring so the
// client still opens and closes the replay window.
func (c *ChainService) sendReplay(session *ClientSession, lines []string, header string, segment bool) {
	// Replay filters join/leave unless the session asked for them.
	// Dates, audits and untagged lines always go. Filtered lines never
	// occupied chain positions, so the replayed window has no gaps.
	var minHeight, maxHeight uint64
	var haveHeight bool
	sent := 0
	dropped := 0

	// Non-blocking sends: the peer may be slow or already dead (WritePump
	// gone) and this runs under BroadcastMu, so blocking here would stall
	// every broadcast. Drops are counted and logged; the trailer reports
	// what was actually queued.
	replaySend := func(b []byte) bool {
		select {
		case session.Send <- b:
			return true
		default:
			dropped++
			return false
		}
	}

	replaySend([]byte(header))
	for _, msgStr := range lines {
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
				if replaySend([]byte(msgStr)) {
					sent++
				}
			} else {
				cleaned := filter.CleanHistoryMessage(msgStr)
				if replaySend([]byte(cleaned)) {
					sent++
				}
			}
		} else {
			cleaned := filter.CleanHistoryMessage(msgStr)
			if replaySend([]byte(cleaned)) {
				sent++
			}
		}
	}
	footer := fmt.Sprintf("--- Kết thúc lịch sử (%d/%d) ---", sent, len(lines))
	if segment {
		footer = fmt.Sprintf("--- Kết thúc lịch sử cũ (%d/%d) ---", sent, len(lines))
		if !haveHeight {
			footer = "--- Kết thúc lịch sử cũ: không còn tin cũ hơn ---"
		}
	}
	replaySend([]byte(footer))
	trailer, _ := json.Marshal(HistorySync{Type: "history_sync", MinHeight: minHeight, MaxHeight: maxHeight,
		Sent: sent, Total: len(lines)})
	replaySend(trailer)
	if dropped > 0 {
		logWarnf("⚠️ [REPLAY] Dropped %d/%d lines for slow peer (buffer full)", dropped, len(lines)+3)
	}
}

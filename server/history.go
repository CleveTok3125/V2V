package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/CleveTok3125/V2V/internal/strutil"
	"time"

	"github.com/CleveTok3125/V2V/internal/filter"
	"github.com/CleveTok3125/V2V/internal/trip"
	"github.com/CleveTok3125/V2V/internal/wire"

	"github.com/gorilla/websocket"
)

// tripChainUpdate classifies how a recovered record relates to the tip
// already stored for its key.
type tripChainUpdate int

const (
	tripChainExtended tripChainUpdate = iota // seq+1 and prev matches: contiguous
	tripChainGap                             // seq jumped ahead: missing records
	tripChainFork                            // seq+1 but prev differs: competing history
	tripChainStale                           // seq not newer: ignore
)

// applyTripChain folds one recovered (already signature-verified) record
// into the tip for its key. Continuity is checked so a truncated log or a
// forked chain is reported, but the higher seq is still adopted: the live
// client that produced it continues from that seq, so refusing it would
// only strand that identity. LastSeen is stamped by the caller.
func applyTripChain(cur TripChain, has bool, seq uint32, prevBytes, newPrev []byte) (TripChain, tripChainUpdate) {
	if !has {
		return TripChain{Seq: seq, PrevHash: newPrev}, tripChainExtended
	}
	if seq <= cur.Seq {
		return cur, tripChainStale
	}
	if seq == cur.Seq+1 && bytes.Equal(prevBytes, cur.PrevHash) {
		return TripChain{Seq: seq, PrevHash: newPrev}, tripChainExtended
	}
	if seq == cur.Seq+1 {
		return TripChain{Seq: seq, PrevHash: newPrev}, tripChainFork
	}
	return TripChain{Seq: seq, PrevHash: newPrev}, tripChainGap
}

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

	var lastSeq uint64
	for i := range records {
		rec := &records[i]
		// Backfill the history cursor for records written before seq
		// existed: assign in load order so a stored line keeps a stable
		// seq across restarts.
		if rec.Seq == 0 {
			lastSeq++
			rec.Seq = lastSeq
		} else if rec.Seq > lastSeq {
			lastSeq = rec.Seq
		}
		if rec.Wire != nil && rec.Wire.Seq == 0 {
			rec.Wire.Seq = rec.Seq
		}
		var tripForChain *TripMeta
		var wireForVerify *WireMessage
		msgForHistory, ok := recordLine(*rec)
		if !ok {
			continue
		}
		if rec.Wire != nil {
			tripForChain = rec.Wire.Trip
			wireForVerify = rec.Wire
		}
		// A signed record is appended only after it verifies: a tampered
		// trip line must not reach the replayed RAM history just because
		// the disk still holds it.
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
			// Fold the record into its key's tip. A gap or fork is
			// reported but the higher seq is still adopted so the live
			// client continues; an older duplicate never rewinds a newer
			// tip.
			cur, has := s.TripChains.Load(tripForChain.Pub)
			var tip TripChain
			if has {
				tip, _ = cur.(TripChain)
			}
			next, status := applyTripChain(tip, has, tripForChain.Seq, prevBytes, newPrev)
			switch status {
			case tripChainGap:
				logWarnf("⛔ [TRIP CHAIN GAP] %s: seq %d follows %d — missing records, adopting tip", strutil.Short(tripForChain.Pub), tripForChain.Seq, tip.Seq)
			case tripChainFork:
				logWarnf("⛔ [TRIP CHAIN FORK] %s: seq %d prev does not continue seq %d, adopting tip", strutil.Short(tripForChain.Pub), tripForChain.Seq, tip.Seq)
			}
			if status != tripChainStale {
				next.LastSeen = time.Now()
				s.TripChains.Store(tripForChain.Pub, next)
			}
		}
		s.Chain.appendMessageToHistory(msgForHistory)
	}

	s.Chain.seq = lastSeq

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

// BroadcastNotice sends a server-originated notification (join/leave)
// that never enters the hash chain: it carries no authorship or ordering
// evidence, only display text. Stored for replay, broadcast live.
// Management lines that must serve as evidence use BroadcastAudit;
// unicast warnings stay raw strings (per-client, never chained).
//
// leaves names the tag leaves, not the finished tag list: they are turned
// into the full root-to-leaf chain here so a caller cannot publish a
// partial chain and quietly escape a mute its parents are subject to.
func (h *Hub) BroadcastNotice(text string, leaves []string, sender *websocket.Conn) {
	h.broadcastSystem(text, leaves, "", sender)
}

// BroadcastDate announces a new calendar day. It carries the day as data
// (sys_date) next to the rendered banner, so a client that already showed
// this day — from the replay tail, or from a server that restarted and
// re-announced it — can recognise the repeat without comparing banner text.
func (h *Hub) BroadcastDate(text, day string, sender *websocket.Conn) {
	h.broadcastSystem(text, []string{wire.TagDate}, day, sender)
}

// serverLocation is the timezone a notice is stamped in. Production always
// resolves a location from config, but a zero Timezone would panic Time.In,
// so fall back to UTC rather than trust the caller.
func serverLocation() *time.Location {
	if loc := Cfg.Static.Timezone; loc != nil {
		return loc
	}
	return time.UTC
}

// powNoticeWire is noticeWire for a notice whose text names a
// proof-of-work tier. The tier travels as a field as well as in the text,
// so the client can apply ui.powMinTier to the notice without parsing the
// banner — the floor says how much work to announce, so a challenge below
// it is exactly the line a user asking for a higher floor wants gone.
func powNoticeWire(text string, tier int, leaves ...string) WireMessage {
	notice := noticeWire(time.Now(), text, leaves...)
	notice.SysPowTier = tier
	return notice
}

// markerWire builds a replay window marker. Unlike a notice it carries no
// server time — it is a frame for the client's replay state machine, not a
// line to read — and the text is only what an older client would print.
// Callers pass the full chain via WithTags, since a marker names both the
// window it opens or closes and, for a footer, how it closed.
func markerWire(tags []string, text string) WireMessage {
	return WireMessage{Type: "system", Tags: tags, Text: text}
}

// noticeWire builds one unchained system line. leaves names the tag leaves
// rather than the finished list, so every producer publishes the same
// root-to-leaf chain and none of them can emit a partial chain that would
// escape a mute on one of its parents.
func noticeWire(now time.Time, text string, leaves ...string) WireMessage {
	now = now.In(serverLocation())
	return WireMessage{
		Type: "system",
		Time: now.Format("15:04"),
		Tags: wire.WithTags(leaves...), Text: text,
	}
}

// broadcastSystem stores and broadcasts one unchained system line. day is
// the machine date a date banner announces, empty for every other notice.
func (h *Hub) broadcastSystem(text string, leaves []string, day string, sender *websocket.Conn) {
	now := time.Now().In(serverLocation())
	// Seq is assigned under Chain.Mu, and the disk enqueue happens under
	// the same lock, so persisted order always matches seq order.
	h.chain.Mu.Lock()
	notice := noticeWire(now, text, leaves...)
	notice.SysDate = day
	notice.Seq = h.chain.assignSeqLocked()
	data, _ := json.Marshal(notice)
	h.chain.appendMessageLocked(string(data))
	if h.chain.Store != nil {
		h.chain.Store.EnqueueWire(notice, now)
	}
	h.chain.Mu.Unlock()
	h.fanout(data, sender, true, false)
}

// BroadcastAudit chains a server-originated management line as evidence:
// unlike notices, audit lines occupy chain positions and verify like
// chat. No producers yet; the route exists so management evidence never
// rides the notice path by mistake.
func (h *Hub) BroadcastAudit(text string, sender *websocket.Conn, serverPub string) {
	now := time.Now().In(serverLocation())
	h.BroadcastMu.Lock()
	defer h.BroadcastMu.Unlock()
	_, data := h.chain.linkAndStore(WireMessage{
		Type: "system", Time: now.Format("15:04"),
		Tags: wire.WithTags(wire.TagAudit), Text: text,
	}, serverPub)

	h.fanout(data, sender, true, false)
}

func (h *Hub) BroadcastWire(wire WireMessage, sender *websocket.Conn, serverPub string) {
	h.BroadcastMu.Lock()
	defer h.BroadcastMu.Unlock()
	_, data := h.chain.linkAndStore(wire, serverPub)
	// Echo to the sender doubles as delivery confirmation so it can
	// replace its grey placeholder with the confirmed rendering.
	h.fanout(data, sender, false, true)
}

// serveHistorySegment serves one on-demand older segment. The expensive
// collection (RAM scan, raw file read, full zstd decode) runs outside
// BroadcastMu so a paging request cannot stall live broadcasts; only the
// send holds the lock, so segment lines and the trailer stay contiguous
// (same ordering contract as the join replay: BroadcastMu -> Chain.Mu,
// never the reverse).
func (s *ChatServer) serveHistorySegment(session *ClientSession, before uint64, limit int) {
	if limit <= 0 {
		return
	}
	// Fail-closed on an out-of-range cursor: a before above the tip can
	// never yield a window, and with HISTORY_DISK_LOOKUP>0 it would
	// otherwise scan/decompress a whole disk generation per request.
	s.Chain.Mu.RLock()
	ready, tip := s.Chain.ready, s.Chain.height
	s.Chain.Mu.RUnlock()
	if before != 0 && ready && before > tip {
		s.Hub.BroadcastMu.Lock()
		s.Chain.sendReplay(session, nil, replaySegment)
		s.Hub.BroadcastMu.Unlock()
		return
	}
	lines := s.Chain.collectSegment(before, limit)
	s.Hub.BroadcastMu.Lock()
	defer s.Hub.BroadcastMu.Unlock()
	s.Chain.sendReplay(session, lines, replaySegment)
}

func (h *Hub) CheckAndBroadcastDate(now time.Time) {
	currentDate := now.Format("02/01/2006")

	h.LastMessageDateMu.Lock()
	defer h.LastMessageDateMu.Unlock()

	if h.LastMessageDate == "" || h.LastMessageDate != currentDate {
		h.LastMessageDate = currentDate

		dateMsg := fmt.Sprintf("\x1b[36m--- Ngày %s ---\x1b[0m", currentDate)

		// Two renderings of the same day on purpose: the banner keeps the
		// display order, sys_date carries the machine value so a client
		// can tell a repeat from a new day.
		h.BroadcastDate(dateMsg, now.Format("2006-01-02"), nil)
	}
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
		var stored WireMessage
		if err := json.Unmarshal([]byte(msgStr), &stored); err == nil && stored.ChainHeight != 0 && stored.ChainHeight >= before {
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
		if rec.Seq == 0 {
			return rec.Message, true
		}
		// A backfilled legacy notice is normalized to a system wire so
		// it carries its history cursor like any other stored line.
		data, _ := json.Marshal(WireMessage{Type: "system", Text: rec.Message, Seq: rec.Seq})
		return string(data), true
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
		var stored WireMessage
		if err := json.Unmarshal([]byte(msgStr), &stored); err != nil || stored.ChainHeight == 0 {
			continue
		}
		if !found || stored.ChainHeight < oldest {
			oldest, found = stored.ChainHeight, true
		}
	}
	if !found {
		return 0
	}
	return oldest
}

// historyInfo summarizes the served RAM window for a joining peer so it
// can start paging without guessing. Only RAM is announced; a peer that
// needs deeper history pages with the before cursor.
func (c *ChainService) historyInfo() HistoryInfo {
	c.Mu.RLock()
	defer c.Mu.RUnlock()
	info := HistoryInfo{Type: "history_info", Count: len(c.History)}
	n := len(c.History)
	if n == 0 {
		return info
	}
	var first, last WireMessage
	if json.Unmarshal([]byte(c.History[0]), &first) == nil {
		info.MinSeq = first.Seq
	}
	if json.Unmarshal([]byte(c.History[n-1]), &last) == nil {
		info.MaxSeq = last.Seq
	}
	info.MinHeight = oldestChained(c.History)
	for i := n - 1; i >= 0; i-- {
		var w WireMessage
		if json.Unmarshal([]byte(c.History[i]), &w) == nil && w.ChainHeight > 0 {
			info.MaxHeight = w.ChainHeight
			break
		}
	}
	// Defensive: a foreign or hand-edited store could be out of order.
	if info.MinSeq > info.MaxSeq {
		info.MinSeq, info.MaxSeq = info.MaxSeq, info.MinSeq
	}
	if info.MinHeight > info.MaxHeight {
		info.MinHeight, info.MaxHeight = info.MaxHeight, info.MinHeight
	}
	return info
}

// collectSegment gathers up to limit stored lines older than before in
// replay order, without sending. before is an exclusive chain height; 0
// means the tail window over the whole history. Unchained lines carry no
// height, so they travel with their neighbors by position: the cutoff is
// the first line at or above before. Lines evicted from RAM are served
// from disk when the lookup level allows (see windowBefore); the RAM
// seam may repeat a few unchained lines, chained content stays exact.
// Kept separate from the send so disk I/O and zstd decode run outside
// BroadcastMu.
func (c *ChainService) collectSegment(before uint64, limit int) []string {
	if limit <= 0 {
		return nil
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
	return ring.lines()
}

// SendChatSegment collects and sends one on-demand segment. An exhausted
// window still terminates the stream (header, plain exhausted footer,
// trailer) so the requester never hangs waiting.
func (c *ChainService) SendChatSegment(session *ClientSession, before uint64, limit int) {
	if limit <= 0 {
		return
	}
	c.sendReplay(session, c.collectSegment(before, limit), replaySegment)
}

// collectAfterSeq returns up to limit stored lines with seq > afterSeq,
// oldest first, plus whether more remain and the resume cursor (the last
// seq returned). RAM only: the recent window is what an initial load or
// a refill needs; deeper history stays on the height-cursor path.
func (c *ChainService) collectAfterSeq(afterSeq uint64, limit int) (lines []string, more bool, next uint64) {
	if limit <= 0 {
		return nil, false, 0
	}
	c.Mu.RLock()
	defer c.Mu.RUnlock()
	for _, msgStr := range c.History {
		var stored WireMessage
		if err := json.Unmarshal([]byte(msgStr), &stored); err != nil || stored.Seq <= afterSeq {
			continue
		}
		if len(lines) == limit {
			return lines, true, next
		}
		lines = append(lines, msgStr)
		next = stored.Seq
	}
	return lines, false, 0
}

// collectBeforeSeq returns up to limit stored lines with seq < beforeSeq,
// newest first (beforeSeq 0 means from the tip), plus whether more remain
// and the resume cursor (the smallest seq returned).
func (c *ChainService) collectBeforeSeq(beforeSeq uint64, limit int) (lines []string, more bool, next uint64) {
	if limit <= 0 {
		return nil, false, 0
	}
	if beforeSeq == 0 {
		beforeSeq = ^uint64(0)
	}
	c.Mu.RLock()
	defer c.Mu.RUnlock()
	for i := len(c.History) - 1; i >= 0; i-- {
		msgStr := c.History[i]
		var stored WireMessage
		if err := json.Unmarshal([]byte(msgStr), &stored); err != nil || stored.Seq == 0 || stored.Seq >= beforeSeq {
			continue
		}
		if len(lines) == limit {
			return lines, true, next
		}
		lines = append(lines, msgStr)
		next = stored.Seq
	}
	return lines, false, 0
}

// serveHistorySeq answers a seq-cursor page. Ascending (after) is
// verified like a join window; descending (before) is render-only older
// history. The collection runs outside BroadcastMu; only the send holds
// it, like segments.
func (s *ChatServer) serveHistorySeq(session *ClientSession, afterSeq, beforeSeq *uint64, limit int) {
	if limit <= 0 {
		return
	}
	var lines []string
	var meta pageMeta
	var mode replayMode
	switch {
	case afterSeq != nil:
		l, more, next := s.Chain.collectAfterSeq(*afterSeq, limit)
		lines, meta, mode = l, pageMeta{Direction: "after", NextSeq: next, More: more}, replayJoin
	case beforeSeq != nil:
		l, more, next := s.Chain.collectBeforeSeq(*beforeSeq, limit)
		lines, meta, mode = l, pageMeta{Direction: "before", NextSeq: next, More: more}, replaySegment
	default:
		return
	}
	s.Hub.BroadcastMu.Lock()
	defer s.Hub.BroadcastMu.Unlock()
	s.Chain.sendReplayPage(session, lines, mode, meta)
}

// collectRanges returns up to limit chained lines whose chain_height
// falls inside one of the ranges, oldest first. Invalid ranges (From 0
// or From > To) are dropped. RAM only: a refill repairs recent drops,
// and deeper history stays on the height-cursor path.
func (c *ChainService) collectRanges(ranges []HeightRange, limit int) []string {
	if limit <= 0 || len(ranges) == 0 {
		return nil
	}
	rs := make([]HeightRange, 0, len(ranges))
	for _, r := range ranges {
		if r.From == 0 || r.From > r.To {
			continue
		}
		rs = append(rs, r)
	}
	if len(rs) == 0 {
		return nil
	}
	sort.Slice(rs, func(i, j int) bool { return rs[i].From < rs[j].From })
	maxTo := rs[len(rs)-1].To
	c.Mu.RLock()
	defer c.Mu.RUnlock()
	var out []string
	for _, msgStr := range c.History {
		var stored WireMessage
		if err := json.Unmarshal([]byte(msgStr), &stored); err != nil {
			continue
		}
		h := stored.ChainHeight
		if h == 0 || h > maxTo {
			continue
		}
		inRange := false
		for _, r := range rs {
			if h >= r.From && h <= r.To {
				inRange = true
				break
			}
		}
		if !inRange {
			continue
		}
		out = append(out, msgStr)
		if len(out) >= limit {
			break
		}
	}
	return out
}

// serveHistoryRanges answers a refill request for exact chained-height
// ranges. The collection runs outside BroadcastMu; only the send holds
// it, like segments.
func (s *ChatServer) serveHistoryRanges(session *ClientSession, ranges []HeightRange, limit int) {
	if limit <= 0 || len(ranges) == 0 {
		return
	}
	if maxR := Cfg.Dynamic.Load().HistoryRefillMaxRanges; maxR > 0 && len(ranges) > maxR {
		ranges = ranges[:maxR]
	}
	lines := s.Chain.collectRanges(ranges, limit)
	s.Hub.BroadcastMu.Lock()
	defer s.Hub.BroadcastMu.Unlock()
	s.Chain.sendReplayPage(session, lines, replayRecovery, pageMeta{})
}

// sendReplay renders stored lines in replay format: header, content,
// human footer with counts, and a HistorySync trailer. lines must be a
// private copy. Sends never block (drops on a full peer buffer are
// counted and logged); the trailer reports what was actually queued.
// A segment footer is labeled as older history, a recovery footer
// as refill history; both say plainly when the window holds no
// chained line (exhausted) instead of a bare zero count. All footer
// variants keep the boundary substring so the client still opens and
// closes the replay window.
// historySyncTrailer encodes the machine-readable trailer closing a
// replay. Dropped is the number of sends the peer's full buffer
// refused: the client skips its fork check when it is non-zero, since
// an incomplete window cannot prove the log changed.
// pageMeta carries the seq-cursor metadata for a paged response. Zero
// values mean "not a seq page": the connect/segment/recovery replays
// keep the legacy trailer fields only.
type pageMeta struct {
	Direction string
	NextSeq   uint64
	More      bool
}

func historySyncTrailer(minHeight, maxHeight uint64, sent, total, dropped int, firstSeq, lastSeq uint64, meta pageMeta) []byte {
	trailer, _ := json.Marshal(HistorySync{Type: "history_sync", MinHeight: minHeight, MaxHeight: maxHeight,
		Sent: sent, Total: total, Dropped: dropped,
		Direction: meta.Direction, FirstSeq: firstSeq, LastSeq: lastSeq, NextSeq: meta.NextSeq, More: meta.More})
	return trailer
}

// replayMode selects the replay footer label: the connect-time join
// burst, an on-demand older segment, or a recovery window answering
// missed heights. Only the label changes; the format is identical.
type replayMode int

const (
	replayJoin replayMode = iota
	replaySegment
	replayRecovery
)

func (c *ChainService) sendReplay(session *ClientSession, lines []string, mode replayMode) {
	c.sendReplayPage(session, lines, mode, pageMeta{})
}

// replayHeader returns the wording and the tag leaf of the marker that
// opens a window of the given kind. The text is what a client draws; the
// tag is what it reads, and deriving both from the mode keeps a wording
// change from drifting away from the window it names.
func replayHeader(mode replayMode) (text string, leaf string) {
	switch mode {
	case replaySegment:
		return "--- Lịch sử cũ ---", wire.TagHistoryOlder
	case replayRecovery:
		return "--- Lịch sử bù ---", wire.TagHistoryRecover
	default:
		return "--- Lịch sử chat gần đây ---", wire.TagHistoryBegin
	}
}

func (c *ChainService) sendReplayPage(session *ClientSession, lines []string, mode replayMode, meta pageMeta) {
	// Replay filters join/leave unless the session asked for them.
	// Dates, audits and untagged lines always go. Filtered lines never
	// occupied chain positions, so the replayed window has no gaps.
	var minHeight, maxHeight uint64
	var haveHeight bool
	var firstSeq, lastSeq uint64
	sent := 0
	dropped := 0

	// Non-blocking sends: the peer may be slow or already dead (WritePump
	// gone) and this runs under BroadcastMu, so blocking here would stall
	// every broadcast. Drops are counted and logged; the trailer reports
	// what was actually queued. Content lines coalesce into multi-line
	// frames (newline-separated JSON) so a bounded window fits the
	// per-session send queue; header, footer and trailer stay separate
	// frames.
	replaySend := func(b []byte) bool {
		select {
		case session.Send <- b:
			return true
		default:
			return false
		}
	}
	batchLines, batchBytes := 32, 16384
	if cfg := Cfg.Dynamic.Load(); cfg != nil {
		if cfg.HistoryReplayBatchLines > 0 {
			batchLines = cfg.HistoryReplayBatchLines
		}
		if cfg.HistoryReplayBatchBytes > 0 {
			batchBytes = cfg.HistoryReplayBatchBytes
		}
	}
	var batch []byte
	batchN := 0
	flushBatch := func() {
		if len(batch) == 0 {
			return
		}
		// Send a copy: the frame outlives this loop and the scratch
		// buffer is reused for the next batch.
		out := make([]byte, len(batch))
		copy(out, batch)
		if replaySend(out) {
			sent += batchN
		} else {
			dropped += batchN
		}
		batch = batch[:0]
		batchN = 0
	}
	addBatched := func(payload string) {
		if batchN > 0 && (batchN >= batchLines || len(batch)+len(payload)+1 > batchBytes) {
			flushBatch()
		}
		batch = append(batch, payload...)
		batch = append(batch, '\n')
		batchN++
	}

	headerText, headerLeaf := replayHeader(mode)
	headerLine, _ := json.Marshal(markerWire(wire.WithTags(headerLeaf), headerText))
	if !replaySend(headerLine) {
		dropped++
	}
	for _, msgStr := range lines {
		// Keep history messages as stored (could be legacy ANSI string or WireMessage JSON)
		// For WireMessage JSON, send as is; for legacy, clean and send
		var stored WireMessage
		wireErr := json.Unmarshal([]byte(msgStr), &stored)
		if wireErr == nil && stored.Type == "system" && !session.WantJoins &&
			wire.HasAnyTag(stored.Tags, wire.TagJoin, wire.TagLeave) {
			continue
		}
		// Window bounds cover sent chained lines only: skipped lines
		// must not widen the range, and unchained notices (height 0)
		// must not drag the minimum to zero.
		if wireErr == nil && stored.ChainHeight > 0 {
			if !haveHeight || stored.ChainHeight < minHeight {
				minHeight = stored.ChainHeight
			}
			if !haveHeight || stored.ChainHeight > maxHeight {
				maxHeight = stored.ChainHeight
			}
			haveHeight = true
		}
		if wireErr == nil && stored.Seq > 0 {
			if firstSeq == 0 {
				firstSeq = stored.Seq
			}
			lastSeq = stored.Seq
		}
		if wireErr == nil {
			// JSON lines are newline-free (Marshal escapes them), so
			// they batch safely.
			if stored.Type == "chat" {
				addBatched(msgStr)
			} else {
				addBatched(filter.CleanHistoryMessage(msgStr))
			}
		} else {
			// Legacy raw lines may embed newlines: flush the batch and
			// send them alone so the peer's split stays exact.
			flushBatch()
			if replaySend([]byte(filter.CleanHistoryMessage(msgStr))) {
				sent++
			} else {
				dropped++
			}
		}
	}
	flushBatch()
	// The footer carries both the window it closes and how it closed, so
	// the client can tell a counted end from an exhausted one without
	// reading the wording.
	window := []string{wire.TagHistory}
	switch mode {
	case replaySegment:
		window = append(window, wire.TagHistoryOlder)
	case replayRecovery:
		window = append(window, wire.TagHistoryRecover)
	}
	footer := fmt.Sprintf("--- Kết thúc lịch sử (%d/%d) ---", sent, len(lines))
	closer := wire.TagHistoryEnd
	if mode != replayJoin {
		noun := "lịch sử cũ"
		exhausted := "--- Kết thúc lịch sử cũ: không còn tin cũ hơn ---"
		if mode == replayRecovery {
			noun = "lịch sử bù"
			exhausted = "--- Kết thúc lịch sử bù: không còn tin trong bộ nhớ ---"
		}
		footer = fmt.Sprintf("--- Kết thúc %s (%d/%d) ---", noun, sent, len(lines))
		if !haveHeight {
			footer = exhausted
			closer = wire.TagHistoryExhausted
		}
	}
	footerLine, _ := json.Marshal(markerWire(wire.WithTags(append(window, closer)...), footer))
	if !replaySend(footerLine) {
		dropped++
	}
	if !replaySend(historySyncTrailer(minHeight, maxHeight, sent, len(lines), dropped, firstSeq, lastSeq, meta)) {
		dropped++
	}
	if dropped > 0 {
		logWarnf("⚠️ [REPLAY] Dropped %d/%d lines for slow peer (buffer full)", dropped, len(lines)+3)
	}
}

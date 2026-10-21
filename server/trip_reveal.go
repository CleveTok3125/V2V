package main

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/CleveTok3125/V2V/lib/guard"
	"github.com/CleveTok3125/V2V/lib/trip"
	"github.com/CleveTok3125/V2V/lib/wire"
)

// Exposed-trip registry bounds. Publishing a seed costs its publisher
// the identity, so arrivals are rare, but minting keys is free: the map
// still needs the same TTL + hard cap treatment as TripChains.
const (
	exposedTripsTTL = 24 * time.Hour
	maxExposedTrips = 10000
)

// exposedEntry remembers when a seed was revealed. Touch-on-use keeps an
// active exposed identity flagged while idle entries age out.
type exposedEntry struct {
	At time.Time
}

// markTripExposed records a published seed at the current time. Only a
// key holder could have produced the announcement, so recording needs
// no further proof.
func (s *ChatServer) markTripExposed(pubHex string) {
	s.markTripExposedAt(pubHex, time.Now())
}

// markTripExposedAt records a published seed at the given timestamp,
// skipping entries that have already aged past the TTL.
func (s *ChatServer) markTripExposedAt(pubHex string, at time.Time) {
	now := time.Now()
	if at.IsZero() {
		at = now
	}
	if now.Sub(at) > exposedTripsTTL {
		return
	}
	s.ExposedTrips.Store(pubHex, exposedEntry{At: at})
	s.pruneExposedTrips(now)
}

// isTripExposed reports whether the pub's seed was revealed. Touches
// active entries so they stay flagged; drops idle ones.
func (s *ChatServer) isTripExposed(pubHex string) bool {
	return s.isTripExposedAt(pubHex, time.Now())
}

func (s *ChatServer) isTripExposedAt(pubHex string, at time.Time) bool {
	v, ok := s.ExposedTrips.Load(pubHex)
	if !ok {
		return false
	}
	e, ok := v.(exposedEntry)
	if !ok {
		s.ExposedTrips.Delete(pubHex)
		return false
	}
	now := time.Now()
	if now.Sub(e.At) > exposedTripsTTL {
		s.ExposedTrips.Delete(pubHex)
		return false
	}
	if at.IsZero() {
		at = now
	}
	if at.After(e.At) && now.Sub(at) <= exposedTripsTTL {
		s.ExposedTrips.Store(pubHex, exposedEntry{At: at})
	}
	return true
}

// pruneExposedTrips drops idle entries and enforces the hard cap,
// evicting the least recently seen first. Mirrors pruneTripChains.
func (s *ChatServer) pruneExposedTrips(now time.Time) {
	type entry struct {
		key string
		at  time.Time
	}
	s.ExposedTripsMu.Lock()
	defer s.ExposedTripsMu.Unlock()
	var kept []entry
	s.ExposedTrips.Range(func(k, v any) bool {
		e, ok := v.(exposedEntry)
		if !ok {
			s.ExposedTrips.Delete(k)
			return true
		}
		if !e.At.IsZero() && now.Sub(e.At) > exposedTripsTTL {
			s.ExposedTrips.Delete(k)
			return true
		}
		key, _ := k.(string)
		kept = append(kept, entry{key: key, at: e.At})
		return true
	})
	if len(kept) <= maxExposedTrips {
		return
	}
	sort.Slice(kept, func(i, j int) bool { return kept[i].at.Before(kept[j].at) })
	for _, e := range kept[:len(kept)-maxExposedTrips] {
		s.ExposedTrips.Delete(e.key)
	}
}

// handleTripReveal records a seed the session publishes for its own
// identity and warns the room. The seed must derive to the session's
// trip pub, so a client cannot expose someone else, and the frame must
// carry the identity's signature over that pub: the stored notice proves
// itself at boot, so a hand-edited history line marks nothing.
func (s *ChatServer) handleTripReveal(session *ClientSession, clientIP, seedHex, sigHex string) {
	if session.TripPub == "" {
		s.unicastNotice(session, "[Hệ thống]: Phiên này không dùng tripcode.", wire.TagTripReveal)
		return
	}
	pubHex := strings.ToLower(session.TripPub)
	if !trip.VerifySeed(seedHex, pubHex) {
		s.observeErr(clientIP, "reveal:seed")
		s.unicastNotice(session, "[Hệ thống]: Seed không khớp danh tính của phiên này.", wire.TagTripReveal)
		return
	}
	if !trip.VerifyReveal(pubHex, sigHex) {
		s.observeErr(clientIP, "reveal:sig")
		s.unicastNotice(session, "[Hệ thống]: Chữ ký công bố không hợp lệ.", wire.TagTripReveal)
		return
	}
	if s.isTripExposed(pubHex) {
		s.unicastNotice(session, "[Hệ thống]: Danh tính này đã được ghi nhận là lộ seed.", wire.TagTripReveal)
		return
	}
	if s.Screener != nil && s.Screener.IsMuted(session.Conn) {
		s.unicastNotice(session, "[Hệ thống]: Chat của bạn tạm dừng chờ xác minh. Hoàn thành thử thách PoW để tiếp tục.", wire.TagPowGate)
		return
	}
	if !s.allowHistoryRequest(clientIP, 1) {
		s.unicastNotice(session, "[Hệ thống]: Thao tác quá nhanh, thử lại sau.", wire.TagLimit)
		return
	}
	s.observeMessage(clientIP)
	badge := session.TripBadge
	if badge == "" {
		badge = guard.TripBadgeFromPubHex(pubHex)
	}
	s.markTripExposed(pubHex)
	s.Hub.BroadcastTripReveal(pubHex, badge, sigHex)
	logInfof("👁 (reveal) Danh tính %s đã lộ seed (công bố bởi %s)", badge, session.DisplayName)
}

// BroadcastTripReveal publishes the room-wide seed-reveal announcement
// and stores it, so a restart rebuilds the registry from the notice. The
// notice carries the identity's signature, which boot re-verifies.
func (h *Hub) BroadcastTripReveal(pubHex, badge, sigHex string) {
	now := time.Now().In(serverLocation())
	notice := noticeWire(now, fmt.Sprintf("\x1b[90m%s\x1b[0m [Hệ thống]: Danh tính %s đã lộ seed, đừng tin badge này nữa.", now.Format("15:04"), badge), wire.TagTripReveal)
	notice.ExposedPub = pubHex
	notice.ExposedSig = strings.ToLower(sigHex)
	h.storeSystem(notice, now, nil)
}

package main

import (
	"github.com/CleveTok3125/V2V/lib/wire"
)

// handleVote folds one vote into the cumulative total for a chained
// message and broadcasts the new total. Votes reuse the history cost
// budget, the tier slowmode and the behavior observation of chat, plus
// a dedicated per-IP cooldown at message cadence.
func (s *ChatServer) handleVote(session *ClientSession, clientIP string, target uint64, direction int) {
	switch direction {
	case wire.DirUpvote, wire.DirDownvote, wire.DirWithdraw:
	default:
		s.observeErr(clientIP, "vote:direction")
		s.unicastNotice(session, "[Hệ thống]: Hướng vote không hợp lệ.", wire.TagEnvelope)
		return
	}
	if target == 0 {
		s.observeErr(clientIP, "vote:target")
		s.unicastNotice(session, "[Hệ thống]: Tin vote không hợp lệ.", wire.TagEnvelope)
		return
	}
	s.Chain.Mu.RLock()
	ready, tip := s.Chain.ready, s.Chain.height
	s.Chain.Mu.RUnlock()
	if ready && target > tip {
		s.observeErr(clientIP, "vote:future")
		s.unicastNotice(session, "[Hệ thống]: Tin vote dẫn tới ID chưa tồn tại.", wire.TagEnvelope)
		return
	}
	if s.Screener != nil && s.Screener.IsMuted(session.Conn) {
		s.unicastNotice(session, "[Hệ thống]: Chat của bạn tạm dừng chờ xác minh. Hoàn thành thử thách PoW để tiếp tục.", wire.TagPowGate)
		return
	}
	if !s.allowHistoryRequest(clientIP, 1) {
		s.unicastNotice(session, "[Hệ thống]: Vote quá nhanh, thử lại sau.", wire.TagLimit)
		return
	}
	// One vote per message cadence per IP, on its own tracker so voting
	// never steals chat cooldown and chatting never steals vote cooldown.
	// Without this the history budget alone (hundreds per second) lets one
	// address spray votes across messages or flip one message nonstop,
	// each vote storing and broadcasting a line.
	if !s.VoteCooldown.Allow(clientIP, Cfg.Dynamic.Load().MessageCooldown) {
		s.unicastNotice(session, "[Hệ thống]: Bạn đang vote quá nhanh, thử lại sau.", wire.TagLimit)
		return
	}
	if !s.allowSlowSend(session, clientIP, Cfg.Dynamic.Load().MessageCooldown) {
		return
	}
	s.observeMessage(clientIP)
	upvotes, downvotes, dup, changed := s.applyVote(clientIP, target, direction)
	if !changed {
		if dup {
			s.observeErr(clientIP, "vote:dup")
		}
		s.unicastNotice(session, "[Hệ thống]: Chưa có vote để hủy.", wire.TagLimit)
		return
	}
	s.Hub.BroadcastVote(target, upvotes, downvotes)
}

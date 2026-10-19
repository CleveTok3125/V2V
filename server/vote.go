package main

import (
	"github.com/CleveTok3125/V2V/lib/wire"
)

// handleVote folds one vote into the cumulative total for a chained
// message and broadcasts the new total. Votes reuse the history cost
// budget, the tier slowmode and the behavior observation of chat:
// no separate limiter exists for them.
func (s *ChatServer) handleVote(session *ClientSession, clientIP string, target uint64, dir int) {
	if dir < -1 || dir > 1 {
		s.observeErr(clientIP, "vote:dir")
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
	if !s.allowSlowSend(session, clientIP, Cfg.Dynamic.Load().MessageCooldown) {
		return
	}
	s.observeMessage(clientIP)
	up, down, dup, changed := s.applyVote(clientIP, target, dir)
	if dup {
		s.observeErr(clientIP, "vote:dup")
		s.unicastNotice(session, "[Hệ thống]: Bạn đã vote tin này rồi.", wire.TagLimit)
		return
	}
	if !changed {
		s.unicastNotice(session, "[Hệ thống]: Chưa có vote để hủy.", wire.TagLimit)
		return
	}
	s.Hub.BroadcastVote(target, up, down)
}

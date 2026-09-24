package main

import (
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/CleveTok3125/V2V/internal/behavior"
	"github.com/CleveTok3125/V2V/internal/pow"
	"github.com/CleveTok3125/V2V/internal/serverconfig"
	"github.com/CleveTok3125/V2V/internal/wire"
)

// ScreenChallenge is one outstanding in-chat PoW challenge, bound to a
// single connection. The stored preset is exactly what was offered, so
// verification never trusts client-supplied parameters.
type ScreenChallenge struct {
	ID      string
	IP      string
	Conn    *websocket.Conn
	Tier    int
	Preset  pow.Preset
	Salt    string
	Expires time.Time
}

// PowScreener tracks in-chat challenges, mutes and per-IP re-check
// timers. All state is mutex-guarded: ReadPump, the scheduler and
// disconnect paths touch it concurrently.
type PowScreener struct {
	mu        sync.Mutex
	engine    *BehaviorEngine
	screens   map[*websocket.Conn]*ScreenChallenge
	byID      map[string]*websocket.Conn
	muted     map[*websocket.Conn]bool
	nextCheck map[string]time.Time
}

// NewPowScreener creates a screener; a nil engine disables scoring
// hooks but keeps challenge bookkeeping working (tests).
func NewPowScreener(engine *BehaviorEngine) *PowScreener {
	return &PowScreener{
		engine:    engine,
		screens:   map[*websocket.Conn]*ScreenChallenge{},
		byID:      map[string]*websocket.Conn{},
		muted:     map[*websocket.Conn]bool{},
		nextCheck: map[string]time.Time{},
	}
}

// Issue records a challenge and clears any mute: a fresh chance to
// prove resets the silence.
func (sc *PowScreener) Issue(conn *websocket.Conn, ip string, tier int, preset pow.Preset, salt, id string, expires time.Time) {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	if old, ok := sc.screens[conn]; ok {
		delete(sc.byID, old.ID)
	}
	sc.screens[conn] = &ScreenChallenge{ID: id, IP: ip, Conn: conn, Tier: tier, Preset: preset, Salt: salt, Expires: expires}
	sc.byID[id] = conn
	delete(sc.muted, conn)
}

// Resolve verifies a result. Success clears the challenge; a wrong
// nonce consumes it (bounding verify cost) without muting — the
// scheduler re-challenges on its own cadence.
func (sc *PowScreener) Resolve(id string, nonce uint64) (*websocket.Conn, bool) {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	conn, ok := sc.byID[id]
	if !ok {
		return nil, false
	}
	ch := sc.screens[conn]
	if ch == nil || time.Now().After(ch.Expires) {
		delete(sc.screens, conn)
		delete(sc.byID, id)
		return nil, false
	}
	if !pow.Verify(ch.Preset, ch.Salt, nonce) {
		delete(sc.screens, conn)
		delete(sc.byID, id)
		return nil, false
	}
	delete(sc.screens, conn)
	delete(sc.byID, id)
	delete(sc.muted, conn)
	return conn, true
}

// Decline drops the challenge and mutes chat sends until a later
// challenge passes.
func (sc *PowScreener) Decline(id string) (*websocket.Conn, bool) {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	conn, ok := sc.byID[id]
	if !ok {
		return nil, false
	}
	delete(sc.screens, conn)
	delete(sc.byID, id)
	sc.muted[conn] = true
	return conn, true
}

// IsMuted reports whether conn may only read.
func (sc *PowScreener) IsMuted(conn *websocket.Conn) bool {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	return sc.muted[conn]
}

// Unmute lifts a mute (a passed challenge already does).
func (sc *PowScreener) Unmute(conn *websocket.Conn) {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	delete(sc.muted, conn)
}

// CheckDeadline splits overdue challenges: kick under attack, mute
// otherwise. Decided challenges leave the books either way.
func (sc *PowScreener) CheckDeadline(now time.Time, underAttack bool) (kick, mute []*websocket.Conn) {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	for conn, ch := range sc.screens {
		if now.Before(ch.Expires) {
			continue
		}
		delete(sc.screens, conn)
		delete(sc.byID, ch.ID)
		if underAttack {
			kick = append(kick, conn)
		} else {
			sc.muted[conn] = true
			mute = append(mute, conn)
		}
	}
	return kick, mute
}

// DropConn forgets everything about a disconnected conn.
func (sc *PowScreener) DropConn(conn *websocket.Conn) {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	if ch, ok := sc.screens[conn]; ok {
		delete(sc.byID, ch.ID)
		delete(sc.screens, conn)
	}
	delete(sc.muted, conn)
}

// Get returns a copy of the outstanding challenge for conn.
func (sc *PowScreener) Get(conn *websocket.Conn) (ScreenChallenge, bool) {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	ch, ok := sc.screens[conn]
	if !ok {
		return ScreenChallenge{}, false
	}
	return *ch, true
}

// behaviorTickInterval paces the re-check sweep; per-IP cadence comes
// from POW_RECHECK_MIN/MAX. behaviorSaveInterval paces store writes.
const (
	behaviorTickInterval = 30 * time.Second
	behaviorSaveInterval = 5 * time.Minute
)

// unicast delivers msg to sess only while it is still registered,
// holding ClientsMu across the liveness check and the send: unregister
// deletes under the same lock, so a send outside it can race
// close(Send) and panic (same convention as alertConcurrentIdentity).
func (s *ChatServer) unicast(sess *ClientSession, msg string) {
	s.Hub.ClientsMu.RLock()
	defer s.Hub.ClientsMu.RUnlock()
	if _, alive := s.Hub.Clients[sess.Conn]; !alive {
		return
	}
	select {
	case sess.Send <- []byte(msg):
	default:
	}
}

// unicastData is unicast for an already-marshaled frame.
func (s *ChatServer) unicastData(sess *ClientSession, data []byte) {
	s.Hub.ClientsMu.RLock()
	defer s.Hub.ClientsMu.RUnlock()
	if _, alive := s.Hub.Clients[sess.Conn]; !alive {
		return
	}
	select {
	case sess.Send <- data:
	default:
	}
}

// behaviorScheduler re-scores connected IPs and hands out PoW.
func (s *ChatServer) behaviorScheduler() {
	tick := time.NewTicker(behaviorTickInterval)
	defer tick.Stop()
	var lastSave, lastStats time.Time
	for now := range tick.C {
		s.behaviorTick(now, &lastSave, &lastStats)
	}
}

func randRecheck(bc *serverconfig.BehaviorConfig) time.Duration {
	if bc.RecheckMax <= bc.RecheckMin {
		return bc.RecheckMin
	}
	return bc.RecheckMin + time.Duration(randInt63n(int64(bc.RecheckMax-bc.RecheckMin)))
}

// behaviorTick scores due sessions, issues challenges, enforces
// deadlines and persists state.
func (s *ChatServer) behaviorTick(now time.Time, lastSave, lastStats *time.Time) {
	a := Cfg.Abuse.Load()
	if a == nil || a.Behavior == nil || s.Behavior == nil || s.Screener == nil {
		return
	}
	bc := a.Behavior
	type entry struct {
		conn *websocket.Conn
		sess *ClientSession
	}
	s.Hub.ClientsMu.RLock()
	list := make([]entry, 0, len(s.Hub.Clients))
	byConn := make(map[*websocket.Conn]*ClientSession, len(s.Hub.Clients))
	for conn, sess := range s.Hub.Clients {
		list = append(list, entry{conn, sess})
		byConn[conn] = sess
	}
	s.Hub.ClientsMu.RUnlock()
	under := s.Attack != nil && s.Attack.IsUnderAttack()
	loc := Cfg.Static.Timezone
	if loc == nil {
		loc = time.UTC
	}
	maxTier := a.PowTierCount - 1
	for _, e := range list {
		ip := e.sess.IP
		if ip == "" || !s.Screener.Due(ip, now) {
			continue
		}
		s.Screener.Defer(ip, now.Add(randRecheck(bc)))
		tier, _ := s.Behavior.Score(ip, bc, now, loc)
		if under {
			tier = behavior.ApplyBump(tier, s.Attack.Scale(), bc.BumpMax, maxTier)
		}
		if tier < 1 || tier > maxTier || s.Screener.Has(e.conn) {
			continue
		}
		s.issueScreenChallenge(e.sess, ip, tier, now, a)
	}
	kick, mute := s.Screener.CheckDeadline(now, under)
	for _, c := range mute {
		if sess, ok := byConn[c]; ok {
			s.unicast(sess, "[Hệ thống]: Quá hạn xác minh, chat tạm dừng cho tới khi bạn hoàn thành thử thách PoW mới.")
		}
	}
	for _, c := range kick {
		if sess, ok := byConn[c]; ok {
			// unregisterClient closes the conn, so ReadPump's defer
			// runs observeDisconnect; calling it here would double-count.
			s.Hub.unregisterClient(sess, sess.IP)
		}
	}
	if now.Sub(*lastSave) >= behaviorSaveInterval {
		ret := map[int]time.Duration{}
		for n, r := range bc.Retention {
			ret[n] = r
		}
		if len(ret) > 0 {
			s.Behavior.Prune(now, ret, ret[0])
		}
		if err := s.Behavior.Save(); err != nil {
			logWarnf("⚠️ [BEHAVIOR] save: %v", err)
		}
		*lastSave = now
	}
	if bc.StatsEnabled && now.Sub(*lastStats) >= bc.StatsWindow {
		if err := s.Behavior.SnapshotStats(s.Behavior.file+".stats", now); err != nil {
			logWarnf("⚠️ [BEHAVIOR] stats: %v", err)
		}
		*lastStats = now
	}
}

// issueScreenChallenge builds a signed offer for one session and
// delivers the notice plus the offer frame (both non-blocking).
func (s *ChatServer) issueScreenChallenge(sess *ClientSession, ip string, tier int, now time.Time, a *serverconfig.AbuseConfig) {
	t := a.PowTiers[tier]
	preset := pow.Preset{Time: t.T, Memory: t.M, Threads: t.P, Difficulty: uint(t.Difficulty)}
	web := sess.Platform == "web"
	preset, _ = pow.Clamp(preset, web)
	id, err := randHex(16)
	if err != nil {
		return
	}
	serverPub := s.serverPub()
	salt := fmt.Sprintf("chat|%s|%s|%s", serverPub, ip, id)
	deadline := now.Add(a.ScreenDeadline)
	if dyn := Cfg.Dynamic.Load(); dyn != nil && dyn.IdleChatTimeout > deadline.Sub(now) {
		deadline = now.Add(dyn.IdleChatTimeout)
	}
	s.Screener.Issue(sess.Conn, ip, tier, preset, salt, id, deadline)
	s.unicast(sess, "[Hệ thống]: Máy chủ yêu cầu xác minh chống spam (mức PoW "+strconv.Itoa(tier)+"). Client đang giải nền, có thể đơ tạm thời — đây là hoạt động bình thường, không phải lỗi.")
	offer := pow.Offer{Tier: tier, Preset: preset, Salt: salt, Expires: deadline.Unix(), ChallengeID: id}
	sig := ""
	if s.ServerID != nil {
		if privBytes, err := hex.DecodeString(s.ServerID.PrivateKey); err == nil && len(privBytes) == ed25519.PrivateKeySize {
			sig = hex.EncodeToString(pow.SignOffer(ed25519.PrivateKey(privBytes), offer))
		}
	}
	data, _ := json.Marshal(wire.PowOffer{
		Type: "pow_offer", Tier: tier,
		T: preset.Time, M: preset.Memory, P: preset.Threads, Difficulty: preset.Difficulty,
		Salt: salt, Expires: deadline.Unix(), ChallengeID: id,
		OfferSig: sig, ServerPub: serverPub,
	})
	s.unicastData(sess, data)
}

// handlePowFrame answers one in-chat PoW result or decline. Unknown
// payloads are ignored quietly (the chat parser rejects them next).
func (s *ChatServer) handlePowFrame(session *ClientSession, raw string) {
	if s.Screener == nil {
		return
	}
	unicast := func(msg string) {
		select {
		case session.Send <- []byte(msg):
		default:
		}
	}
	if strings.Contains(raw, `"pow_result"`) {
		var res wire.PowResult
		if err := json.Unmarshal([]byte(raw), &res); err != nil || res.Type != "pow_result" {
			return
		}
		conn, ok := s.Screener.Resolve(res.ChallengeID, res.Nonce)
		if ok && conn == session.Conn {
			unicast("[Hệ thống]: Xác minh PoW xong, chat mở lại bình thường.")
		} else {
			unicast("[Hệ thống]: Đáp án PoW không hợp lệ, bạn sẽ nhận thử thách mới.")
		}
		return
	}
	if strings.Contains(raw, `"pow_decline"`) {
		var dec wire.PowDecline
		if err := json.Unmarshal([]byte(raw), &dec); err != nil || dec.Type != "pow_decline" {
			return
		}
		if _, ok := s.Screener.Decline(dec.ChallengeID); ok {
			unicast("[Hệ thống]: Đã ghi nhận từ chối, chat tạm dừng cho tới khi xác minh.")
		}
	}
}

// Has reports whether conn has an outstanding challenge.
func (sc *PowScreener) Has(conn *websocket.Conn) bool {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	_, ok := sc.screens[conn]
	return ok
}

// Due reports whether ip is due for a re-check at now.
func (sc *PowScreener) Due(ip string, now time.Time) bool {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	next, ok := sc.nextCheck[ip]
	return !ok || !now.Before(next)
}

// Defer pushes the next re-check for ip to t.
func (sc *PowScreener) Defer(ip string, t time.Time) {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	sc.nextCheck[ip] = t
}

// ForgetCheck drops the re-check timer (disconnect cleanup).
func (sc *PowScreener) ForgetCheck(ip string) {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	delete(sc.nextCheck, ip)
}

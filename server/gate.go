package main

import (
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/CleveTok3125/V2V/internal/behavior"
	"github.com/CleveTok3125/V2V/internal/guard"
	"github.com/CleveTok3125/V2V/internal/pow"
)

// gateChallengeCooldown spaces challenge issuance per IP: fetching a
// challenge is cheap, solving it is not, so issuance itself needs a
// light throttle. Internal constant like the auth timeouts.
const gateChallengeCooldown = 2 * time.Second

// SubmitCode classifies a gate submission.
type SubmitCode int

const (
	SubmitOK SubmitCode = iota
	SubmitEarly
	SubmitBad
	SubmitGone
)

// GateChallenge is one issued PoW bundle. Salt binds challenge, server
// and IP; Ticket binds IP, challenge and earliest time.
type GateChallenge struct {
	ID       string
	Tier     int
	Preset   pow.Preset
	Salt     string
	Earliest time.Time
	Expires  time.Time
	Ticket   string
	Used     bool
}

// GatePass is a lease: IP-bound, TTL-capped, optionally single-use.
type GatePass struct {
	Token   string
	IP      string
	Expires time.Time
}

// GateStore issues challenges and lease passes. Challenges are
// single-use (a wrong solution consumes them, forcing a re-solve);
// passes live until TTL.
type GateStore struct {
	mu             sync.Mutex
	key            []byte
	challenges     map[string]*GateChallenge
	passes         map[string]*GatePass
	cooldown       *guard.CooldownMap
	statusCooldown *guard.CooldownMap
}

// NewGateStore creates a store with a fresh per-boot HMAC key:
// restart invalidates outstanding tickets and passes.
func NewGateStore() *GateStore {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		key = []byte(fmt.Sprintf("fallback-%d", time.Now().UnixNano()))
	}
	return &GateStore{
		key:            key,
		challenges:     map[string]*GateChallenge{},
		passes:         map[string]*GatePass{},
		cooldown:       guard.NewCooldownMap(),
		statusCooldown: guard.NewCooldownMap(),
	}
}

// AllowStatus throttles the stateless status probe per IP so it cannot
// become a flood target. It does O(1) work and returns one boolean.
func (g *GateStore) AllowStatus(ip string) bool {
	return g.statusCooldown.Allow(ip, time.Second)
}

func (g *GateStore) ticket(ip, id string, earliest time.Time) string {
	m := hmac.New(sha256.New, g.key)
	fmt.Fprintf(m, "%s|%s|%d", ip, id, earliest.UnixNano())
	return hex.EncodeToString(m.Sum(nil))
}

func randHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// Request issues a challenge bundle. waitMin/Max bound the earliest
// solve time; ttl bounds the challenge itself.
func (g *GateStore) Request(ip string, tier int, preset pow.Preset, waitMin, waitMax, ttl time.Duration, serverPub string) (*GateChallenge, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.cooldown.Allow(ip, gateChallengeCooldown) {
		return nil, fmt.Errorf("challenge too soon")
	}
	id, err := randHex(16)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	wait := waitMin
	if waitMax > waitMin {
		wait += time.Duration(randInt63n(int64(waitMax - waitMin)))
	}
	earliest := now.Add(wait)
	salt := fmt.Sprintf("v1|%s|%s|%s", serverPub, ip, id)
	c := &GateChallenge{
		ID:       id,
		Tier:     tier,
		Preset:   preset,
		Salt:     salt,
		Earliest: earliest,
		Expires:  earliest.Add(ttl),
	}
	c.Ticket = g.ticket(ip, id, earliest)
	g.challenges[id] = c
	return c, nil
}

func randInt63n(n int64) int64 {
	if n <= 0 {
		return 0
	}
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return 0
	}
	v := int64(b[0])<<56 | int64(b[1])<<48 | int64(b[2])<<40 | int64(b[3])<<32 |
		int64(b[4])<<24 | int64(b[5])<<16 | int64(b[6])<<8 | int64(b[7])
	if v < 0 {
		v = -v
	}
	return v % n
}

// ChallengeEarliest returns the earliest solve time for Retry-After.
func (g *GateStore) ChallengeEarliest(id string) (time.Time, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	c, ok := g.challenges[id]
	if !ok || c.Used {
		return time.Time{}, false
	}
	return c.Earliest, true
}

// Submit verifies ticket, clock, PoW and consumes the challenge. A
// wrong solution consumes it (forcing a re-solve); an early clock
// does not (the same bundle stays usable).
func (g *GateStore) Submit(ip, id string, nonce uint64, ticket string, passTTL time.Duration) (*GatePass, SubmitCode) {
	g.mu.Lock()
	defer g.mu.Unlock()
	c, ok := g.challenges[id]
	if !ok || c.Used || time.Now().After(c.Expires) {
		delete(g.challenges, id)
		return nil, SubmitGone
	}
	if !hmac.Equal([]byte(ticket), []byte(g.ticket(ip, id, c.Earliest))) {
		delete(g.challenges, id)
		return nil, SubmitBad
	}
	if time.Now().Before(c.Earliest) {
		return nil, SubmitEarly
	}
	if !pow.Verify(c.Preset, c.Salt, nonce) {
		delete(g.challenges, id)
		return nil, SubmitBad
	}
	delete(g.challenges, id)
	token, err := randHex(32)
	if err != nil {
		return nil, SubmitGone
	}
	p := &GatePass{Token: token, IP: ip, Expires: time.Now().Add(passTTL)}
	g.passes[token] = p
	return p, SubmitOK
}

// UsePass validates a lease pass for ip. Single-use passes are
// consumed; reusable ones live until TTL.
func (g *GateStore) UsePass(ip, token string, singleUse bool) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	p, ok := g.passes[token]
	if !ok || p.IP != ip || time.Now().After(p.Expires) {
		return false
	}
	if singleUse {
		delete(g.passes, token)
	}
	return true
}

// Sweep drops expired passes and challenges.
func (g *GateStore) Sweep(now time.Time) {
	g.mu.Lock()
	defer g.mu.Unlock()
	for tok, p := range g.passes {
		if now.After(p.Expires) {
			delete(g.passes, tok)
		}
	}
	for id, c := range g.challenges {
		if now.After(c.Expires) {
			delete(g.challenges, id)
		}
	}
}

// gateTier resolves the pre-connect challenge tier: the configured
// gate tier plus the attack bump when behavior scoring is on.
func (s *ChatServer) gateTier() int {
	a := Cfg.Abuse.Load()
	if a == nil || a.PowTierCount <= 1 {
		return 0
	}
	maxTier := a.PowTierCount - 1
	tier := a.PowGateTier
	if tier < 0 {
		tier = 0
	}
	if tier > maxTier {
		tier = maxTier
	}
	if a.Behavior != nil && s.Attack != nil {
		tier = behavior.ApplyBump(tier, s.Attack.Scale(), a.Behavior.BumpMax, maxTier)
	}
	return tier
}

// gateRequired reports whether a new handshake must present a gate
// pass: always in "always" mode, or only while under attack.
func (s *ChatServer) gateRequired() bool {
	a := Cfg.Abuse.Load()
	if a == nil || s.Gate == nil {
		return false
	}
	if a.PowFirstConnect == "always" {
		return true
	}
	return a.PowFirstConnect == "under-attack" && s.Attack != nil && s.Attack.IsUnderAttack()
}

// checkGatePass validates the handshake pass (query or header). A
// missing or spent pass answers 429 with the gate address.
func (s *ChatServer) checkGatePass(w http.ResponseWriter, r *http.Request, clientIP string) bool {
	tok := r.URL.Query().Get("gate_pass")
	if tok == "" {
		tok = r.Header.Get("X-V2V-Pass")
	}
	a := Cfg.Abuse.Load()
	single := a != nil && a.PassSingleUse
	if s.Gate == nil || !s.Gate.UsePass(clientIP, tok, single) {
		if s.Attack != nil {
			s.Attack.noteGate()
		}
		s.observeHTTP(clientIP, "join_ws", http.StatusTooManyRequests)
		w.Header().Set("Retry-After", "5")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_ = json.NewEncoder(w).Encode(map[string]any{"required": true, "gate": "/api/join-gate"})
		return false
	}
	return true
}

// httpGateFor reports whether class needs a lease pass right now:
// listed in GATE_HTTP_CLASSES and either under attack
// (GATE_HTTP_MODE=under-attack) or flagged by the behavior engine
// (GATE_HTTP_MODE=tier, tier >= GATE_HTTP_TIER_MIN). An unscored
// HTTP-only IP counts as tier 0, so clean clients never see a pass.
func (s *ChatServer) httpGateFor(class, ip string) bool {
	a := Cfg.Abuse.Load()
	if a == nil || !a.GateHTTPEnabled || s.Gate == nil {
		return false
	}
	listed := false
	for _, c := range a.GateHTTPClasses {
		if c == class {
			listed = true
			break
		}
	}
	if !listed {
		return false
	}
	if a.GateHTTPMode == "tier" {
		if s.Behavior == nil {
			return false
		}
		tier, ok := s.Behavior.Tier(ip)
		return ok && tier >= a.GateHTTPTierMin
	}
	return s.Attack != nil && s.Attack.IsUnderAttack()
}

// checkHTTPPass enforces the lease pass on heavy endpoints. HTTP
// passes are always reusable leases (solving per request is absurd),
// never consumed.
func (s *ChatServer) checkHTTPPass(w http.ResponseWriter, r *http.Request, class, clientIP string) bool {
	if !s.httpGateFor(class, clientIP) {
		return true
	}
	tok := r.URL.Query().Get("pass")
	if tok == "" {
		tok = r.Header.Get("X-V2V-Pass")
	}
	if !s.Gate.UsePass(clientIP, tok, false) {
		w.Header().Set("Retry-After", "5")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_ = json.NewEncoder(w).Encode(map[string]any{"required": true, "gate": "/api/join-gate"})
		return false
	}
	return true
}

type gateRequest struct {
	Step        string `json:"step"`
	UI          string `json:"ui"`
	ChallengeID string `json:"challenge_id"`
	Nonce       uint64 `json:"nonce"`
	Ticket      string `json:"ticket"`
}

// handleJoinGate serves the pre-connect gate: step=challenge issues a
// signed PoW bundle, step=submit verifies it into a lease pass.
func (s *ChatServer) handleJoinGate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if rejectUntrustedProxy(w, r) {
		return
	}
	clientIP := getClientIP(r)
	a := Cfg.Abuse.Load()
	if a == nil {
		http.Error(w, "gate unavailable", http.StatusServiceUnavailable)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64*1024)
	var req gateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	switch req.Step {
	case "status":
		if !s.Gate.AllowStatus(clientIP) {
			w.WriteHeader(http.StatusTooManyRequests)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": "slow down"})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"required": s.gateRequired()})
	case "challenge":
		tier := s.gateTier()
		var preset pow.Preset
		if tier > 0 && tier < len(a.PowTiers) {
			t := a.PowTiers[tier]
			preset = pow.Preset{Time: t.T, Memory: t.M, Threads: t.P, Difficulty: uint(t.Difficulty)}
		}
		web := req.UI == "web"
		preset, _ = pow.Clamp(preset, web)
		serverPub := s.serverPub()
		c, err := s.Gate.Request(clientIP, tier, preset, a.JoinWaitMin, a.JoinWaitMax, a.PowTTL, serverPub)
		if err != nil {
			w.WriteHeader(http.StatusTooManyRequests)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": "challenge too soon, slow down"})
			return
		}
		offer := pow.Offer{Tier: tier, Preset: preset, Salt: c.Salt, Expires: c.Expires.Unix(), ChallengeID: c.ID}
		sig := ""
		if s.ServerID != nil {
			if privBytes, err := hex.DecodeString(s.ServerID.PrivateKey); err == nil && len(privBytes) == ed25519.PrivateKeySize {
				sig = hex.EncodeToString(pow.SignOffer(ed25519.PrivateKey(privBytes), offer))
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"challenge_id": c.ID,
			"tier":         tier,
			"pow":          map[string]any{"t": preset.Time, "m": preset.Memory, "p": preset.Threads, "difficulty": preset.Difficulty},
			"salt":         c.Salt,
			"earliest":     c.Earliest.Unix(),
			"expires":      c.Expires.Unix(),
			"ticket":       c.Ticket,
			"offer_sig":    sig,
			"server_pub":   serverPub,
		})
	case "submit":
		pass, code := s.Gate.Submit(clientIP, req.ChallengeID, req.Nonce, req.Ticket, a.PassTTL)
		switch code {
		case SubmitOK:
			_ = json.NewEncoder(w).Encode(map[string]any{"pass": pass.Token, "expires": pass.Expires.Unix()})
		case SubmitEarly:
			earliest, _ := s.Gate.ChallengeEarliest(req.ChallengeID)
			retry := int(time.Until(earliest).Seconds()) + 1
			if retry < 1 {
				retry = 1
			}
			w.Header().Set("Retry-After", fmt.Sprintf("%d", retry))
			w.WriteHeader(http.StatusTooManyRequests)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": "too early", "retry_after": retry})
		case SubmitGone:
			w.WriteHeader(http.StatusGone)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": "challenge expired or consumed"})
		default:
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": "invalid solution"})
		}
	default:
		http.Error(w, "unknown step", http.StatusBadRequest)
	}
}

// attackSampler folds handshake pressure every second and logs
// under-attack transitions. It also sweeps gate state.
func (s *ChatServer) attackSampler() {
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	var wasUnder bool
	for now := range tick.C {
		a := Cfg.Abuse.Load()
		if a == nil || s.Attack == nil {
			continue
		}
		st := s.Attack.sample(s.overCap(), now, SampleParams{
			EnterSecs: int(a.AttackEnterSecs / time.Second),
			EnterRPS:  a.AttackEnterRPS,
			TTL:       a.UnderAttackTTL,
			Force:     a.UnderAttackForce,
			ScaleMode: a.EffectiveScaleMode(),
			ScaleW:    a.EffectiveScaleW(),
		})
		if st.Under && !wasUnder {
			logWarnf("🚨 UNDER-ATTACK ENTER (scale=%.2f)", st.Scale)
		}
		if !st.Under && wasUnder {
			logWarnf("✅ UNDER-ATTACK EXIT (quiet %v)", a.UnderAttackTTL)
		}
		wasUnder = st.Under
		if s.Gate != nil {
			s.Gate.Sweep(now)
		}
	}
}

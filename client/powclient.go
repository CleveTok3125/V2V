package main

import (
	"bytes"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/CleveTok3125/V2V/internal/pow"
	"github.com/CleveTok3125/V2V/internal/wire"
)

// Client proof-of-work: pre-connect join gate and in-chat screening.
// Offers carry a server signature; the client verifies it against the
// pinned server identity when one is known, clamps the preset to the
// platform and the local pow budget, and declines anything beyond it
// instead of burning the machine.

// gateChallenge is one /api/join-gate challenge bundle.
type gateChallenge struct {
	ID        string
	Tier      int
	Preset    pow.Preset
	Salt      string
	Earliest  time.Time
	Expires   time.Time
	Ticket    string
	OfferSig  string
	ServerPub string
}

func parseGateChallenge(body []byte) (*gateChallenge, error) {
	var raw struct {
		ChallengeID string `json:"challenge_id"`
		Tier        int    `json:"tier"`
		Pow         struct {
			T          int  `json:"t"`
			M          int  `json:"m"`
			P          int  `json:"p"`
			Difficulty uint `json:"difficulty"`
		} `json:"pow"`
		Salt      string `json:"salt"`
		Earliest  int64  `json:"earliest"`
		Expires   int64  `json:"expires"`
		Ticket    string `json:"ticket"`
		OfferSig  string `json:"offer_sig"`
		ServerPub string `json:"server_pub"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, err
	}
	if raw.ChallengeID == "" || raw.Salt == "" || raw.Ticket == "" {
		return nil, errors.New("incomplete gate challenge")
	}
	return &gateChallenge{
		ID:        raw.ChallengeID,
		Tier:      raw.Tier,
		Preset:    pow.Preset{Time: raw.Pow.T, Memory: raw.Pow.M, Threads: raw.Pow.P, Difficulty: raw.Pow.Difficulty},
		Salt:      raw.Salt,
		Earliest:  time.Unix(raw.Earliest, 0),
		Expires:   time.Unix(raw.Expires, 0),
		Ticket:    raw.Ticket,
		OfferSig:  raw.OfferSig,
		ServerPub: raw.ServerPub,
	}, nil
}

// clientPlatform reports the runtime for PoW preset selection.
func clientPlatform() string {
	if isWASMRuntime() {
		return "web"
	}
	return "native"
}

// verifyPowOffer checks the server signature against the pinned
// identity when one is known (empty pin means trust-on-first-use),
// then clamps the preset to the platform. Tampering with tier or
// difficulty breaks the signature.
func verifyPowOffer(o wire.PowOffer, pin string, wasm bool) (pow.Preset, error) {
	preset := pow.Preset{Time: o.T, Memory: o.M, Threads: o.P, Difficulty: o.Difficulty}
	if pin != "" {
		pub, err := hex.DecodeString(pin)
		if err != nil || len(pub) != ed25519.PublicKeySize {
			return pow.Preset{}, errors.New("bad server pin")
		}
		sig, err := hex.DecodeString(o.OfferSig)
		if err != nil {
			return pow.Preset{}, errors.New("bad offer signature")
		}
		po := pow.Offer{
			Tier: o.Tier, Preset: preset,
			Salt: o.Salt, Expires: o.Expires, ChallengeID: o.ChallengeID,
		}
		if !pow.VerifyOffer(ed25519.PublicKey(pub), po, sig) {
			return pow.Preset{}, errors.New("offer signature mismatch")
		}
	}
	clamped, _ := pow.Clamp(preset, wasm)
	return clamped, nil
}

// decideOffer accepts tiers within the local cap.
func decideOffer(tier, maxTier int) bool {
	return tier <= maxTier
}

// solveWithBudget solves via the platform path (native goroutine, wasm
// worker) under a wall-clock budget, so a malicious offer cannot burn
// the machine.
func solveWithBudget(p pow.Preset, salt string, maxCostMs int64) (uint64, error) {
	return platformSolvePoW(p, salt, maxCostMs)
}

// gateHTTPBase strips the WS path/scheme down to the HTTP origin so
// the gate endpoint stays on the same server the client dials.
func gateHTTPBase(wsURL string) string {
	base := wsURL
	if i := strings.Index(base, "://"); i >= 0 {
		scheme, rest := base[:i], base[i+3:]
		httpScheme := "http"
		if scheme == "wss" || scheme == "https" {
			httpScheme = "https"
		}
		base = httpScheme + "://" + rest
	}
	base = strings.TrimRight(base, "/")
	if i := strings.Index(base, "/"); i >= 0 {
		// Keep host only when the path is exactly the WS endpoint;
		// otherwise preserve sub-path deployments.
		if strings.HasSuffix(base, "/ws") {
			return strings.TrimSuffix(base, "/ws")
		}
		return base
	}
	return base
}

// httpPostJSON posts a bounded JSON body and returns the bounded
// response body with its status. Server-supplied, so both directions
// are size-capped.
func httpPostJSON(url string, body any, timeout time.Duration) ([]byte, int, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, 0, err
	}
	client := &http.Client{Timeout: timeout}
	resp, err := client.Post(url, "application/json", bytes.NewReader(raw))
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	out, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if err != nil {
		return nil, 0, err
	}
	return out, resp.StatusCode, nil
}

// gateRequiredNow asks the server whether the join gate is active.
func gateRequiredNow(httpBase string) bool {
	body, status, err := httpPostJSON(httpBase+"/api/join-gate",
		map[string]any{"step": "status", "ui": clientPlatform()}, 15*time.Second)
	if err != nil || status != http.StatusOK {
		return false
	}
	var out struct {
		Required bool `json:"required"`
	}
	if json.Unmarshal(body, &out) != nil {
		return false
	}
	return out.Required
}

// runGateFlow performs one join-gate round: challenge, verify, solve,
// wait for earliest, submit (with one early retry). It returns the
// lease pass on success. pin is the optional pinned server pubkey;
// empty means trust-on-first-use.
func runGateFlow(httpBase, pin string) (string, bool) {
	body, status, err := httpPostJSON(httpBase+"/api/join-gate",
		map[string]any{"step": "challenge", "ui": clientPlatform()}, 30*time.Second)
	if err != nil || status != http.StatusOK {
		fmt.Printf("❌ Gate challenge thất bại (HTTP %d): %v\n", status, err)
		return "", false
	}
	chal, err := parseGateChallenge(body)
	if err != nil {
		fmt.Printf("❌ Gate challenge sai định dạng: %v\n", err)
		return "", false
	}
	maxTier, maxCostMs := powLocalCap()
	preset, err := verifyPowOffer(wire.PowOffer{
		Tier: chal.Tier, T: chal.Preset.Time, M: chal.Preset.Memory,
		P: chal.Preset.Threads, Difficulty: chal.Preset.Difficulty,
		Salt: chal.Salt, Expires: chal.Expires.Unix(), ChallengeID: chal.ID,
		OfferSig: chal.OfferSig, ServerPub: chal.ServerPub,
	}, pin, isWASMRuntime())
	if err != nil {
		fmt.Printf("❌ Offer PoW không hợp lệ: %v\n", err)
		return "", false
	}
	if !decideOffer(chal.Tier, maxTier) {
		fmt.Printf("❌ Từ chối PoW tier %d (vượt cap %d).\n", chal.Tier, maxTier)
		return "", false
	}
	fmt.Printf("🧩 Giải PoW (tier %d)…\n", chal.Tier)
	nonce, err := solveWithBudget(preset, chal.Salt, maxCostMs)
	if err != nil {
		fmt.Printf("❌ %v\n", err)
		return "", false
	}
	for attempt := 0; attempt < 2; attempt++ {
		if wait := time.Until(chal.Earliest); wait > 0 {
			fmt.Printf("⏳ Chờ cửa vào (%v)…\n", wait.Round(time.Second))
			time.Sleep(wait)
		}
		sub, _ := json.Marshal(map[string]any{
			"step": "submit", "challenge_id": chal.ID,
			"nonce": nonce, "ticket": chal.Ticket,
		})
		respBody, status, err := httpPostJSON(httpBase+"/api/join-gate",
			json.RawMessage(sub), 30*time.Second)
		if err != nil {
			fmt.Printf("❌ Gate submit thất bại: %v\n", err)
			return "", false
		}
		if status == http.StatusOK {
			var out struct {
				Pass string `json:"pass"`
			}
			if err := json.Unmarshal(respBody, &out); err != nil || out.Pass == "" {
				fmt.Printf("❌ Gate pass sai định dạng.\n")
				return "", false
			}
			return out.Pass, true
		}
		var early struct {
			RetryAfter int `json:"retry_after"`
		}
		if status == http.StatusTooManyRequests && json.Unmarshal(respBody, &early) == nil && early.RetryAfter > 0 && attempt == 0 {
			time.Sleep(time.Duration(early.RetryAfter) * time.Second)
			continue
		}
		fmt.Printf("❌ Gate từ chối (HTTP %d).\n", status)
		return "", false
	}
	return "", false
}

// powLocalCap reads the client PoW budget (defaults when unconfigured).
func powLocalCap() (maxTier int, maxCostMs int64) {
	maxTier, maxCostMs = 3, 120000
	if ClientCfg != nil {
		if ClientCfg.Pow.MaxTier > 0 {
			maxTier = ClientCfg.Pow.MaxTier
		}
		if ClientCfg.Pow.MaxCostMs > 0 {
			maxCostMs = ClientCfg.Pow.MaxCostMs
		}
	}
	return maxTier, maxCostMs
}

// sendJSON writes one frame with the socket mutex held.
func (s *Session) sendJSON(v any) error {
	s.SendMu.Lock()
	defer s.SendMu.Unlock()
	return s.Conn.WriteJSON(v)
}

// handlePowOffer verifies, solves and answers one in-chat challenge.
// The pump runs it in its own goroutine, so blocking here is fine. A
// single in-flight guard ignores extra offers: a hostile server must
// not trigger unbounded concurrent memory-hard solves.
func (s *Session) handlePowOffer(offer wire.PowOffer) {
	if !s.powBusy.CompareAndSwap(false, true) {
		return
	}
	defer s.powBusy.Store(false)

	maxTier, maxCostMs := powLocalCap()
	pin := ""
	if s.Challenge.ServerPubKey != "" {
		pin = s.Challenge.ServerPubKey
	}
	preset, err := verifyPowOffer(offer, pin, isWASMRuntime())
	if err != nil {
		fmt.Printf("\n| [Local]: Offer PoW không hợp lệ (%v) — bỏ qua.\n", err)
		return
	}
	if !decideOffer(offer.Tier, maxTier) {
		fmt.Printf("\n| [Local]: Từ chối PoW tier %d (vượt cap %d).\n", offer.Tier, maxTier)
		_ = s.sendJSON(wire.PowDecline{Type: "pow_decline", ChallengeID: offer.ChallengeID})
		return
	}
	fmt.Printf("\n| [Local]: Đang giải PoW (tier %d)…\n", offer.Tier)
	nonce, err := solveWithBudget(preset, offer.Salt, maxCostMs)
	if err != nil {
		fmt.Printf("\n| [Local]: Bỏ PoW (%v).\n", err)
		_ = s.sendJSON(wire.PowDecline{Type: "pow_decline", ChallengeID: offer.ChallengeID})
		return
	}
	if err := s.sendJSON(wire.PowResult{Type: "pow_result", ChallengeID: offer.ChallengeID, Nonce: nonce}); err != nil {
		fmt.Printf("\n| [Local]: Gửi đáp án PoW thất bại: %v\n", err)
	}
}

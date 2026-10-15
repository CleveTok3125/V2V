package main

import (
	"fmt"
	"time"

	"github.com/CleveTok3125/V2V/lib/clientcore"
	"github.com/CleveTok3125/V2V/lib/wire"
)

// Client proof-of-work: the join-gate wiring and the in-chat screening
// handler. The gate round itself lives in lib/clientcore; this file supplies
// the runtime, the budget and the wording.

// clientPlatform reports the runtime for PoW preset selection.
func clientPlatform() string { return clientcore.PlatformName(isWASMRuntime()) }

// gateConfig wires the core's join gate to this client: the transport that
// rides the session proxy, the platform solver and the local budget.
func gateConfig(pin string) clientcore.GateConfig {
	maxTier, maxCostMs := powLocalCap()
	return clientcore.GateConfig{
		Transport: serverHTTPTransport,
		Pin:       pin,
		WASM:      isWASMRuntime(),
		MaxTier:   maxTier,
		MaxCostMs: maxCostMs,
		Solve:     platformSolvePoW,
		Output:    cliGateOutput{},
	}
}

// cliGateOutput renders the core's gate events in the client's language. The
// solving line obeys the same notify gate as other local feedback.
type cliGateOutput struct{}

func (cliGateOutput) OnGate(e clientcore.GateEvent) {
	switch e.Kind {
	case clientcore.GateChallengeFailed:
		fmt.Printf("❌ Gate challenge thất bại (HTTP %d): %v\n", e.Status, e.Err)
	case clientcore.GateChallengeMalformed:
		fmt.Printf("❌ Gate challenge sai định dạng: %v\n", e.Err)
	case clientcore.GateOfferInvalid:
		fmt.Printf("❌ Offer PoW không hợp lệ: %v\n", e.Err)
	case clientcore.GateTierRefused:
		fmt.Printf("❌ Từ chối PoW tier %d (vượt cap %d).\n", e.Tier, e.Cap)
	case clientcore.GateSolving:
		if notifyPowWanted(e.Tier) {
			fmt.Printf("🧩 Giải PoW (tier %d)…\n", e.Tier)
		}
	case clientcore.GateSolveFailed:
		fmt.Printf("❌ %v\n", e.Err)
	case clientcore.GateWaiting:
		fmt.Printf("⏳ Chờ cửa vào (%v)…\n", e.Wait.Round(time.Second))
	case clientcore.GateSubmitFailed:
		fmt.Printf("❌ Gate submit thất bại: %v\n", e.Err)
	case clientcore.GatePassMalformed:
		fmt.Printf("❌ Gate pass sai định dạng.\n")
	case clientcore.GateRefused:
		fmt.Printf("❌ Gate từ chối (HTTP %d).\n", e.Status)
	}
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
	preset, err := clientcore.VerifyPowOffer(offer, pin, isWASMRuntime())
	if err != nil {
		s.Display.DisplayMu.Lock()
		s.emitLocalFeedback(fmt.Sprintf("| [Local]: Offer PoW không hợp lệ (%v) — bỏ qua.\n", err))
		s.Display.DisplayMu.Unlock()
		return
	}
	if !clientcore.DecideOffer(offer.Tier, maxTier) {
		s.Display.DisplayMu.Lock()
		s.emitLocalFeedback(fmt.Sprintf("| [Local]: Từ chối PoW tier %d (vượt cap %d).\n", offer.Tier, maxTier))
		s.Display.DisplayMu.Unlock()
		_ = s.sendJSON(wire.PowDecline{Type: "pow_decline", ChallengeID: offer.ChallengeID})
		return
	}
	s.Display.DisplayMu.Lock()
	s.Verify.AutoVerifyMu.Lock()
	s.Verify.PowTier = offer.Tier
	s.Verify.AutoVerifyMu.Unlock()
	s.Display.DisplayMu.Unlock()
	s.refreshTerminal()
	nonce, err := platformSolvePoW(preset, offer.Salt, maxCostMs)
	s.Display.DisplayMu.Lock()
	s.Verify.AutoVerifyMu.Lock()
	s.Verify.PowTier = 0
	s.Verify.AutoVerifyMu.Unlock()
	s.Display.DisplayMu.Unlock()
	s.refreshTerminal()
	if err != nil {
		s.Display.DisplayMu.Lock()
		s.emitLocalFeedback(fmt.Sprintf("| [Local]: Bỏ PoW (%v).\n", err))
		s.Display.DisplayMu.Unlock()
		_ = s.sendJSON(wire.PowDecline{Type: "pow_decline", ChallengeID: offer.ChallengeID})
		return
	}
	if err := s.sendJSON(wire.PowResult{Type: "pow_result", ChallengeID: offer.ChallengeID, Nonce: nonce}); err != nil {
		s.Display.DisplayMu.Lock()
		s.emitLocalFeedback(fmt.Sprintf("| [Local]: Gửi đáp án PoW thất bại: %v\n", err))
		s.Display.DisplayMu.Unlock()
	}
}

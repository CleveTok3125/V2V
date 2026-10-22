package main

import (
	"crypto/ed25519"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/CleveTok3125/V2V/lib/trip"
	"github.com/CleveTok3125/V2V/lib/wire"
)

// cmdBurn runs the seed-reveal flow. /burn arms it; /burn confirm prints
// the seed and tells the server, which warns the room. The seed is the
// identity's private key, so nothing is shown before the warning.
func (s *Session) cmdBurn(text string) bool {
	if text != "/burn" && text != "/burn confirm" {
		return false
	}
	s.Display.DisplayMu.Lock()
	if len(s.TripPriv) != ed25519.PrivateKeySize {
		s.emitLocalFeedback("[Local]: Phiên này không dùng tripcode, không có gì để công bố.\n")
		s.Display.DisplayMu.Unlock()
		s.refreshTerminal()
		return true
	}
	if text == "/burn" || !s.BurnArmed {
		s.BurnArmed = true
		s.emitLocalFeedback("[Local]: Seed là khóa bí mật của danh tính này; hiện ra là bắt đầu lộ.\n" +
			"  Gõ /burn confirm để hiện và công bố seed cho cả phòng; seed sẽ lưu trong lịch sử server. Sau đó /quit và không dùng lại passphrase/salt.\n")
		s.Display.DisplayMu.Unlock()
		s.refreshTerminal()
		return true
	}
	seedHex := hex.EncodeToString(s.TripPriv[:ed25519.SeedSize])
	pubHex := hex.EncodeToString(s.TripPub)
	sigHex := trip.SignReveal(s.TripPriv, pubHex)
	s.BurnArmed = false
	s.emitLocalFeedback("[Local]: Seed đã hiện — danh tính coi như đã lộ:\n" +
		"  seed: " + seedHex + "\n" +
		"  pub:  " + pubHex + " (" + s.TripBadge + ")\n" +
		"  /quit ngay và không dùng lại passphrase/salt này.\n")
	s.Display.DisplayMu.Unlock()
	sendErr := s.sendJSON(wire.RevealRequest{Type: "trip_reveal", Seed: seedHex, Sig: sigHex})
	s.Display.DisplayMu.Lock()
	if sendErr != nil {
		s.emitStatusLocked(fmt.Sprintf("[Local]: Không gửi được thông báo lộ seed: %v. Cả phòng chưa nhận được minh chứng.\n", sendErr))
	} else {
		s.emitStatusLocked("[Local]: Server đã công bố seed cho cả phòng.\n")
	}
	s.Display.DisplayMu.Unlock()
	s.refreshTerminal()
	return true
}

// cmdBurnVerify checks a published seed against a pubkey. Local only.
func (s *Session) cmdBurnVerify(text string) bool {
	if !(text == "/burn-verify" || strings.HasPrefix(text, "/burn-verify ")) {
		return false
	}
	rest := strings.TrimSpace(strings.TrimPrefix(text, "/burn-verify"))
	fields := strings.Fields(rest)
	s.Display.DisplayMu.Lock()
	if len(fields) != 2 {
		s.emitLocalFeedback("[Local]: Dùng /burn-verify <seedHex> <pubHex>.\n")
		s.Display.DisplayMu.Unlock()
		s.refreshTerminal()
		return true
	}
	if trip.VerifySeed(fields[0], fields[1]) {
		s.emitLocalFeedback("[Local]: Seed khớp pub — danh tính này đã lộ.\n")
	} else {
		s.emitLocalFeedback("[Local]: Seed không khớp pub.\n")
	}
	s.Display.DisplayMu.Unlock()
	s.refreshTerminal()
	return true
}

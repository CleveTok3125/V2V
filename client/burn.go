package main

import (
	"crypto/ed25519"
	"encoding/hex"
	"strings"

	"github.com/CleveTok3125/V2V/lib/trip"
)

// cmdBurn starts the self-destruct flow. The seed is the private key of
// this identity, so nothing is shown or copied yet: displaying it
// exposes it (shoulder surfing, screenshots, scrollback), which is the
// reveal itself. Local only, never sent.
func (s *Session) cmdBurn(text string) bool {
	if text == "/burn" {
		s.Display.DisplayMu.Lock()
		if len(s.TripPriv) != ed25519.PrivateKeySize {
			s.emitLocalFeedback("[Local]: Phiên này không dùng tripcode, không có gì để hủy.\n")
			s.Display.DisplayMu.Unlock()
			s.refreshTerminal()
			return true
		}
		s.emitLocalFeedback("[Local]: Seed là khóa bí mật của danh tính này — hiện ra màn hình là bắt đầu lộ (ai nhìn thấy cũng mạo danh được).\n" +
			"  Gõ /burn confirm nếu chắc chắn muốn tự hủy. Sau khi lộ: gửi seed lên phòng chat, /quit ngay và không dùng lại passphrase/salt này.\n")
		s.Display.DisplayMu.Unlock()
		s.refreshTerminal()
		return true
	}
	if text != "/burn confirm" {
		return false
	}
	s.Display.DisplayMu.Lock()
	if len(s.TripPriv) != ed25519.PrivateKeySize {
		s.emitLocalFeedback("[Local]: Phiên này không dùng tripcode, không có gì để hủy.\n")
		s.Display.DisplayMu.Unlock()
		s.refreshTerminal()
		return true
	}
	seedHex := hex.EncodeToString(s.TripPriv[:ed25519.SeedSize])
	pubHex := hex.EncodeToString(s.TripPub)
	s.emitLocalFeedback("[Local]: Seed đã hiện — coi như danh tính đã lộ từ lúc này:\n" +
		"  seed: " + seedHex + "\n" +
		"  pub:  " + pubHex + " (" + s.TripBadge + ")\n" +
		"  Bôi đen dòng seed để copy, rồi gửi nguyên seed đó trong MỘT tin nhắn (không kèm chữ khác) để server thông báo hủy cho cả phòng. Sau đó /quit ngay và không dùng lại passphrase/salt này.\n")
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
		s.emitLocalFeedback("[Local]: Seed khớp pub — danh tính này đã lộ (ai cũng mạo danh được).\n")
	} else {
		s.emitLocalFeedback("[Local]: Seed không khớp pub — không phải bằng chứng hủy.\n")
	}
	s.Display.DisplayMu.Unlock()
	s.refreshTerminal()
	return true
}

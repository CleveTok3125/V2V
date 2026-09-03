package guard

import (
	"crypto/sha256"
	"encoding/hex"
)

func GenerateTripcode(secret string, length int) string {
	if secret == "" || length <= 0 {
		return ""
	}
	h := sha256.Sum256([]byte(secret))
	fullHex := hex.EncodeToString(h[:])
	if length > len(fullHex) {
		length = len(fullHex)
	}
	return "◆ " + fullHex[:length]
}

func TripBadgeFromPubHex(pubHex string) string {
	b, err := hex.DecodeString(pubHex)
	if err != nil || len(b) == 0 {
		return ""
	}
	h := sha256.Sum256(b)
	return "◆ " + hex.EncodeToString(h[:])[:8]
}

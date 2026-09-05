package tripcolor

import (
	"crypto/sha256"
	"fmt"
)

// Fixed palette for badges — rotated by hash(pub) to avoid harsh reds.
// Colors are 38;2 truecolor, chosen for contrast on #101014 background.
var palette = [][3]int{
	{79, 129, 255},  // blue
	{129, 199, 132}, // green
	{255, 183, 77},  // orange
	{149, 117, 205}, // purple
	{77, 208, 225},  // cyan
	{255, 138, 101}, // peach
	{174, 213, 129}, // light green
	{144, 164, 174}, // blue-gray
	{255, 213, 79},  // amber
	{100, 181, 246}, // light blue
}

// BadgeColor returns ANSI 38;2;R;G;Bm by rotating the fixed palette.
func BadgeColor(badge string) string {
	return BadgeColorWithPalette(badge, palette)
}

// BadgeColorWithPalette is BadgeColor with a caller-supplied palette
// (e.g. from client config). Out-of-range channels are clamped; an empty
// palette falls back to the fixed one so output is never uncolored.
func BadgeColorWithPalette(badge string, pal [][3]int) string {
	if len(pal) == 0 {
		pal = palette
	}
	h := sha256.Sum256([]byte(badge))
	c := pal[int(h[0])%len(pal)]
	return fmt.Sprintf("\x1b[38;2;%d;%d;%dm", clamp(c[0]), clamp(c[1]), clamp(c[2]))
}

func clamp(v int) int {
	if v < 0 {
		return 0
	}
	if v > 255 {
		return 255
	}
	return v
}

// CanonicalPayload is the signed payload for trip messages, shared by client/server.
// displayName is the final server-rendered name (e.g. "[Admin] Anonymous" or "Anonymous#abcd") — binding it prevents displayName spoofing.
// tmpID is the sender's per-session message counter — binding it makes server-side ID tampering fail verification.
func CanonicalPayload(serverPub string, seq uint32, prev []byte, msgHash []byte, pub []byte, displayName string, tmpID uint64) []byte {
	return []byte(fmt.Sprintf("%s\x00%d\x00%x\x00%x\x00%x\x00%s\x00%d", serverPub, seq, prev, msgHash, pub, displayName, tmpID))
}

// CanonicalPayloadLegacy is the pre-chain payload encoding (no tmpID).
// It exists only so pre-upgrade history and legacy browser verify links
// keep verifying read-only; nothing new is ever signed with it.
func CanonicalPayloadLegacy(serverPub string, seq uint32, prev []byte, msgHash []byte, pub []byte, displayName string) []byte {
	return []byte(fmt.Sprintf("%s\x00%d\x00%x\x00%x\x00%x\x00%s", serverPub, seq, prev, msgHash, pub, displayName))
}

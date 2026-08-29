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
	h := sha256.Sum256([]byte(badge))
	idx := int(h[0]) % len(palette)
	c := palette[idx]
	return fmt.Sprintf("\x1b[38;2;%d;%d;%dm", c[0], c[1], c[2])
}

// CanonicalPayload is the signed payload for trip messages, shared by client/server.
// displayName is the final server-rendered name (e.g. "[Admin] Anonymous" or "Anonymous#abcd") — binding it prevents displayName spoofing.
func CanonicalPayload(serverPub string, seq uint32, prev []byte, msgHash []byte, pub []byte, displayName string) []byte {
	return []byte(fmt.Sprintf("%s\x00%d\x00%x\x00%x\x00%x\x00%s", serverPub, seq, prev, msgHash, pub, displayName))
}

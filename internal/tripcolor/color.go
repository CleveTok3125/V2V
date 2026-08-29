package tripcolor

import (
	"crypto/sha256"
	"fmt"
)

// BadgeColor returns ANSI 38;2;R;G;Bm for a badge string deterministically.
func BadgeColor(badge string) string {
	h := sha256.Sum256([]byte(badge))
	hue := float64(h[0]) / 255.0 * 360
	sat := 0.6 + float64(h[1]%51)/255.0*0.3
	light := 0.6
	c := (1 - abs(light*2-1)) * sat
	x := c * (1 - abs(mod(hue/60, 2)-1))
	m := light - c/2
	var r1, g1, b1 float64
	switch {
	case hue < 60:
		r1, g1, b1 = c, x, 0
	case hue < 120:
		r1, g1, b1 = x, c, 0
	case hue < 180:
		r1, g1, b1 = 0, c, x
	case hue < 240:
		r1, g1, b1 = 0, x, c
	case hue < 300:
		r1, g1, b1 = x, 0, c
	default:
		r1, g1, b1 = c, 0, x
	}
	r := int((r1 + m) * 255)
	g := int((g1 + m) * 255)
	b := int((b1 + m) * 255)
	return fmt.Sprintf("\x1b[38;2;%d;%d;%dm", r, g, b)
}

func abs(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}

func mod(a, b float64) float64 {
	return a - b*float64(int(a/b))
}

// CanonicalPayload is the signed payload for trip messages, shared by client/server.
func CanonicalPayload(serverPub string, seq uint32, prev []byte, msgHash []byte, pub []byte) []byte {
	return []byte(fmt.Sprintf("%s\x00%d\x00%x\x00%x\x00%x", serverPub, seq, prev, msgHash, pub))
}

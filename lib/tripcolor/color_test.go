package tripcolor

import (
	"fmt"
	"strings"
	"testing"
)

func TestBadgeRGBMatchesColor(t *testing.T) {
	badge := "abcd1234"
	r, g, b := BadgeRGB(badge)
	if want := fmt.Sprintf("\x1b[38;2;%d;%d;%dm", r, g, b); BadgeColor(badge) != want {
		t.Fatalf("BadgeColor = %q, want %q", BadgeColor(badge), want)
	}
	if r2, g2, b2 := BadgeRGBWithPalette(badge, [][3]int{{300, -5, 128}}); r2 != 255 || g2 != 0 || b2 != 128 {
		t.Fatalf("clamped = %d,%d,%d", r2, g2, b2)
	}
	if rr, gg, bb := BadgeRGBWithPalette(badge, nil); rr != r || gg != g || bb != b {
		t.Fatalf("empty palette must fall back to the fixed one")
	}
}

func TestBadgeColorWithPaletteCustom(t *testing.T) {
	got := BadgeColorWithPalette("◆ abc12345", [][3]int{{1, 2, 3}})
	if !strings.Contains(got, "1;2;3") {
		t.Fatalf("custom palette not applied, got %q", got)
	}
	if got == BadgeColor("◆ abc12345") {
		t.Fatalf("custom palette should differ from default for this badge")
	}
}

func TestBadgeColorWithPaletteFallback(t *testing.T) {
	if got := BadgeColorWithPalette("◆ abc12345", nil); got != BadgeColor("◆ abc12345") {
		t.Fatalf("empty palette should fall back to default, got %q", got)
	}
	if got := BadgeColorWithPalette("◆ abc12345", [][3]int{{-5, 300, 128}}); !strings.Contains(got, "0;255;128") {
		t.Fatalf("channels should be clamped, got %q", got)
	}
}

// The same badge always maps to the same palette slot, and distinct
// badges spread (no single-slot collapse over a sample).
func TestBadgeColor_Deterministic(t *testing.T) {
	a := BadgeColor("◆ deadbeef")
	if a != BadgeColor("◆ deadbeef") {
		t.Fatal("badge color not deterministic")
	}
	seen := map[string]bool{}
	for i := 0; i < 50; i++ {
		seen[BadgeColor("◆ badge"+string(rune('a'+i)))] = true
	}
	if len(seen) < 3 {
		t.Fatalf("palette collapsed to %d colors over 50 badges", len(seen))
	}
}

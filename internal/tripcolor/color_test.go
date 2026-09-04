package tripcolor

import (
	"strings"
	"testing"
)

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

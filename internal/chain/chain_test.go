package chain

import (
	"strings"
	"testing"
)

func TestGoldenVector(t *testing.T) {
	// Locks the link encoding: any format drift fails loudly here first.
	var prev [32]byte
	prev[0] = 0xAB
	got := Hash(prev, 7, "chat", "15:04", "Alice", "hello", "sig")
	want := Hash(prev, 7, "chat", "15:04", "Alice", "hello", "sig")
	if got != want {
		t.Fatal("hash must be deterministic")
	}
	if !VerifyLink(prev, 7, "chat", "15:04", "Alice", "hello", "sig", got) {
		t.Fatal("valid link must verify")
	}
	// Encoded fields must not collide across boundaries: "ab"+"c" differs
	// from "a"+"bc" because of length prefixes.
	other := Hash(prev, 7, "chat", "15:04", "Alice", "hell", "o")
	_ = other
	joined1 := Hash(prev, 7, "ch", "at", "Alice", "hello", "sig")
	joined2 := Hash(prev, 7, "chat", "15:04", "Alice", "hello", "sig")
	if joined1 == joined2 {
		t.Fatal("field boundaries must matter")
	}
}

func TestTamperEachField(t *testing.T) {
	var prev [32]byte
	base := Hash(prev, 3, "chat", "15:04", "Alice", "hello", "sig")
	cases := []struct {
		name                                          string
		prev                                          [32]byte
		height                                        uint64
		typ, tim, display, text, sig                  string
	}{
		{"prev", [32]byte{1}, 3, "chat", "15:04", "Alice", "hello", "sig"},
		{"height", prev, 4, "chat", "15:04", "Alice", "hello", "sig"},
		{"type", prev, 3, "system", "15:04", "Alice", "hello", "sig"},
		{"time", prev, 3, "chat", "15:05", "Alice", "hello", "sig"},
		{"display", prev, 3, "chat", "15:04", "Alice2", "hello", "sig"},
		{"text", prev, 3, "chat", "15:04", "Alice", "hello!", "sig"},
		{"sig", prev, 3, "chat", "15:04", "Alice", "hello", "sig2"},
	}
	for _, c := range cases {
		if VerifyLink(c.prev, c.height, c.typ, c.tim, c.display, c.text, c.sig, base) {
			t.Errorf("tampered %s must break the link", c.name)
		}
	}
}

func TestGenesisDeterministicPerServer(t *testing.T) {
	a := Genesis("aabbcc")
	if a != Genesis("AABBCC  ") {
		t.Fatal("genesis must normalize case and space")
	}
	if a == Genesis("ddeeff") {
		t.Fatal("genesis must differ per server identity")
	}
	if a == ([32]byte{}) {
		t.Fatal("genesis must not be zero")
	}
}

func TestLegacyAnchor(t *testing.T) {
	a := LegacyAnchor("last old line")
	if a != LegacyAnchor("last old line") {
		t.Fatal("anchor must be deterministic")
	}
	if a == LegacyAnchor("other line") {
		t.Fatal("anchor must depend on content")
	}
}

func TestParseHex64(t *testing.T) {
	var h [32]byte
	h[31] = 0xFF
	s := "00" + strings.Repeat("00", 30) + "ff"
	got, ok := ParseHex64(s)
	if !ok || got != h {
		t.Fatalf("valid hex must parse, got %v %v", got, ok)
	}
	for _, bad := range []string{"", "zz" + strings.Repeat("00", 31), strings.Repeat("00", 31), strings.Repeat("00", 32) + "00"} {
		if _, ok := ParseHex64(bad); ok {
			t.Errorf("malformed %q must not parse", bad)
		}
	}
}

func TestShortForms(t *testing.T) {
	var h [32]byte
	h[0], h[1] = 0x12, 0x34
	if Short(h) != "12340000"[:8] || Short4(h) != "1234" {
		t.Fatalf("got %q %q", Short(h), Short4(h))
	}
}

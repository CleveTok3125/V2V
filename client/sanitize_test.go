package main

// Hardening tests: server-controlled fields must never carry terminal
// escape sequences into the rendered output. The shared string filter
// (internal/filter) is covered by its own tests; these pin the client
// call sites that compose raw fields with client-generated SGR/OSC8.

import (
	"strings"
	"testing"

	"github.com/CleveTok3125/V2V/internal/chain"
	"github.com/CleveTok3125/V2V/internal/config"
)

func withDefaultClientCfg(t *testing.T) {
	t.Helper()
	old := ClientCfg
	ClientCfg = config.DefaultClientConfig()
	t.Cleanup(func() { ClientCfg = old })
}

func TestMetaLineForRejectsNonHexHash(t *testing.T) {
	got := metaLineFor(7, "\x1b]52;c;AAAA\x1b\\", "")
	if strings.Contains(got, "\x1b") {
		t.Fatalf("escape leaked: %q", got)
	}
	if got != "  └─  #7:" {
		t.Fatalf("got %q, want %q", got, "  └─  #7:")
	}
}

func TestFormatQuoteRichSanitizesFields(t *testing.T) {
	wire := chainedTripWire(mustTestTrip(t), "hello base msg")
	wire.Time = "15:04\x1b[2J"
	wire.DisplayName = "Alice\x1b]52;c;AAAA\x1b\\"
	got := formatQuoteRich(wire, false, 80)
	if strings.Contains(got, "\x1b[2J") || strings.Contains(got, "52;c") {
		t.Fatalf("escape leaked: %q", got)
	}
	if !strings.Contains(got, "15:04") || !strings.Contains(got, "Alice") {
		t.Fatalf("visible text lost: %q", got)
	}
}

func TestFormatInfoBlockSanitizesFields(t *testing.T) {
	wire := chainedTestWire(chain.Genesis("srv"), 12, 5)
	wire.Time = "15:04\x1b[2J"
	wire.DisplayName = "Alice\x1b]52;c;AAAA\x1b\\"
	wire.ChainHash = "\x1b[31mnothex"
	wire.ChainPrev = "\x1b]8;;javascript:x\x1b\\"
	wire.Trip = &TripMeta{
		Pub: "\x1b]52;c;x", Prev: "zz", Sig: "\x1b[1m",
		ServerPub: "nope", MsgHash: "bad",
	}
	joined := strings.Join(formatInfoBlock(wire), "")
	if strings.Contains(joined, "\x1b") {
		t.Fatalf("escape leaked:\n%s", joined)
	}
}

func TestBuildChatBlockSanitizesHead(t *testing.T) {
	withDefaultClientCfg(t)
	sess := NewSession()
	sess.WSURL = "wss://chat.example.com/ws"
	wire := WireMessage{
		Type:        "chat",
		Time:        "15:04\x1b[2J",
		DisplayName: "Alice\x1b]52;c;AAAA\x1b\\",
		Text:        "hi",
	}
	_, head, _, _, _ := sess.buildChatBlock(wire, true, false)
	if strings.Contains(head, "\x1b[2J") || strings.Contains(head, "52;c") {
		t.Fatalf("escape leaked: %q", head)
	}
	if !strings.Contains(head, "15:04") || !strings.Contains(head, "Alice") || !strings.Contains(head, "hi") {
		t.Fatalf("visible text lost: %q", head)
	}
}

func TestBadgeForWireURLSanitized(t *testing.T) {
	sess := NewSession()
	sess.WSURL = "wss://chat.example.com/ws"
	wire := WireMessage{
		Type:        "chat",
		Time:        "15:04",
		DisplayName: "Bob\x1b]52;c;AAAA\x1b\\",
		Text:        "hi",
		ChainHash:   "abcd",
		ChainHeight: 3,
		Trip: &TripMeta{
			Pub: "aa11\x1b[31m", Seq: 1, Prev: "bb22\x1b]8;;x",
			Sig: "cc33", ServerPub: "ee55", MsgHash: "dd44",
		},
	}
	_, urlStr := sess.badgeForWire(wire, true)
	if !strings.HasPrefix(urlStr, "https://chat.example.com/api/trip/verify?") {
		t.Fatalf("url not absolute/trusted: %q", urlStr)
	}
	if strings.Contains(urlStr, "\x1b") {
		t.Fatalf("escape leaked into url: %q", urlStr)
	}
}

func TestServerHelpersNeutralizeEscapes(t *testing.T) {
	if got := serverField("Alice\x1b]52;c;x\x1b\\\nBob"); strings.Contains(got, "\x1b") || strings.Contains(got, "\n") {
		t.Fatalf("serverField leaked: %q", got)
	}
	if got := serverText("body\x1b]52;c;x\x1b\\tail"); strings.Contains(got, "52;c") {
		t.Fatalf("serverText leaked: %q", got)
	}
}

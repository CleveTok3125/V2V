package main

// Hardening tests: server-controlled fields must never carry terminal
// escape sequences into the rendered output. The shared string filter
// (internal/filter) is covered by its own tests; these pin the client
// call sites that compose raw fields with client-generated SGR/OSC8.

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/CleveTok3125/V2V/internal/chain"
	"github.com/CleveTok3125/V2V/internal/config"
	"github.com/gorilla/websocket"
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

// dialBodyText bounds the read and neutralizes escapes in a failed
// dial's response body.
func TestDialBodyText(t *testing.T) {
	if got := dialBodyText(nil); got != "" {
		t.Fatalf("nil response = %q, want empty", got)
	}
	resp := &http.Response{Body: io.NopCloser(strings.NewReader(
		"  nope\x1b]52;c;AAAA\x1b\\  "))}
	if got := dialBodyText(resp); strings.Contains(got, "52;c") || strings.Contains(got, "\x1b") {
		t.Fatalf("escape leaked: %q", got)
	} else if got != "nope" {
		t.Fatalf("got %q, want %q", got, "nope")
	}
	big := &http.Response{Body: io.NopCloser(strings.NewReader(
		strings.Repeat("x", dialErrorBody+4096)))}
	if got := dialBodyText(big); len(got) != dialErrorBody {
		t.Fatalf("body not capped: len=%d want=%d", len(got), dialErrorBody)
	}
}

func TestClientReadLimit(t *testing.T) {
	withDefaultClientCfg(t)
	want := int64(ClientCfg.Limits.MaxHistoryBytes) + (1 << 20)
	if got := clientReadLimit(); got != want {
		t.Fatalf("clientReadLimit = %d, want %d", got, want)
	}
	// No config must still bound the frame at the margin.
	old := ClientCfg
	ClientCfg = nil
	got := clientReadLimit()
	ClientCfg = old
	if got != 1<<20 {
		t.Fatalf("nil config limit = %d, want %d", got, 1<<20)
	}
}

// A frame over the client read limit must end the stream instead of being
// copied into the Go queue; a frame under it still travels.
func TestReadLimitRejectsOversizeFrame(t *testing.T) {
	withDefaultClientCfg(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		up := websocket.Upgrader{}
		c, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()
		_ = c.WriteMessage(websocket.TextMessage, []byte(strings.Repeat("a", 64)))
		// Second frame blows the limit the client sets below.
		_ = c.WriteMessage(websocket.TextMessage, []byte(strings.Repeat("b", 4096)))
		<-time.After(500 * time.Millisecond)
	}))
	defer srv.Close()

	conn, _, err := websocket.DefaultDialer.Dial(
		"ws://"+strings.TrimPrefix(srv.URL, "http://"), nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	conn.SetReadLimit(1024)

	if _, data, err := conn.ReadMessage(); err != nil || len(data) != 64 {
		t.Fatalf("small frame: len=%d err=%v", len(data), err)
	}
	if _, _, err := conn.ReadMessage(); err == nil {
		t.Fatal("oversize frame must fail the read")
	}
}

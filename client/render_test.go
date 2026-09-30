package main

import (
	"net/url"
	"strings"
	"testing"

	"github.com/CleveTok3125/V2V/internal/filter"
)

func TestParseHistoryBoundary(t *testing.T) {
	// Sync tracking must not depend on the join-display toggle: with -j
	// the old gated block never set inSync, silently disabling the fork
	// check and feeding replay lines to echo matching.
	if b, start := parseHistoryBoundary("| --- Lịch sử chat gần đây ---"); !b || !start {
		t.Errorf("header = (%v,%v), want (true,true)", b, start)
	}
	if b, start := parseHistoryBoundary("| --- Lịch sử cũ ---"); !b || !start {
		t.Errorf("segment header = (%v,%v), want (true,true)", b, start)
	}
	if b, start := parseHistoryBoundary("| --- Kết thúc lịch sử (32/142) ---"); !b || start {
		t.Errorf("footer = (%v,%v), want (true,false)", b, start)
	}
	if b, _ := parseHistoryBoundary("| 12:00 Alice: hello"); b {
		t.Error("chat line detected as boundary")
	}
	if b, _ := parseHistoryBoundary(""); b {
		t.Error("empty line detected as boundary")
	}
}

func TestIsJoinLeaveTagFirst(t *testing.T) {
	if !isJoinLeave(WireMessage{Type: "system", SysKind: "join", Text: "unrelated"}) {
		t.Error("tagged join must match regardless of text")
	}
	if !isJoinLeave(WireMessage{Type: "system", SysKind: "leave", Text: "unrelated"}) {
		t.Error("tagged leave must match regardless of text")
	}
	if isJoinLeave(WireMessage{Type: "system", SysKind: "audit", Text: "x đã tham gia"}) {
		t.Error("audit must not match even with join-like text")
	}
	if isJoinLeave(WireMessage{Type: "system", SysKind: "date", Text: "x"}) {
		t.Error("date must not match")
	}
	// Legacy untagged lines fall back to text sniffing.
	if !isJoinLeave(WireMessage{Type: "system", Text: "12:00 [Hệ thống]: a đã tham gia phòng chat!"}) {
		t.Error("untagged join text must match")
	}
	if isJoinLeave(WireMessage{Type: "system", Text: "plain notice"}) {
		t.Error("plain text must not match")
	}
}

func TestIsDateBannerTagFirst(t *testing.T) {
	if !isDateBanner(WireMessage{Type: "system", SysKind: "date", Text: "x"}) {
		t.Error("tagged date must match")
	}
	if isDateBanner(WireMessage{Type: "system", SysKind: "join", Text: "--- Ngày x ---"}) {
		t.Error("join must not match even with date-like text")
	}
	if !isDateBanner(WireMessage{Type: "system", Text: "--- Ngày 01/01/2026 ---"}) {
		t.Error("untagged date text must match")
	}
}

func TestNormalizeURL(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"example.com:8080", "wss://example.com:8080/ws"},
		{"example.com:8080/chat", "wss://example.com:8080/chat"},
		{"http://h", "ws://h/ws"},
		{"https://h/", "wss://h/ws"},
		{"ws://h/", "ws://h/ws"},
		{"wss://h/ws", "wss://h/ws"},
		{"  wss://h/ws  ", "wss://h/ws"},
		// Scheme is case-insensitive per RFC: must not gain a prefix
		// (host case itself is preserved, net/url semantics).
		{"HTTP://H", "ws://H/ws"},
		// Unknown scheme: leave untouched instead of mangling.
		{"ftp://h", "ftp://h"},
		// An inner http:// in the path is not the scheme: https still
		// becomes wss, but the path survives byte-identical.
		{"https://proxy/http://internal", "wss://proxy/http://internal"},
	}
	for _, c := range cases {
		if got := normalizeURL(c.in); got != c.want {
			t.Errorf("normalizeURL(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// Bare URLs in received chat text must render as OSC8 hyperlinks: the
// local echo path already linkifies, and xterm.js on the web build has
// no built-in URL matcher, so without this a pasted verify link shows
// as plain text and is not clickable.
func TestRenderChatTextLinkifiesBareURL(t *testing.T) {
	out := renderChatText("see https://example.com/x now")
	if !strings.Contains(out, "\x1b]8;;https://example.com/x\x1b\\") {
		t.Fatalf("bare URL not wrapped in OSC8: %q", out)
	}
	// Visible characters are unchanged (cell arithmetic relies on it).
	if strings.ReplaceAll(out, "\x1b", "") == out {
		t.Fatal("expected escape sequences in output")
	}
}

func TestRenderChatTextKeepsMarkupAndLinks(t *testing.T) {
	out := renderChatText("**bold** and https://example.com/y")
	if !strings.Contains(out, "\x1b[1m") {
		t.Fatalf("bold markup lost: %q", out)
	}
	if !strings.Contains(out, "\x1b]8;;https://example.com/y\x1b\\") {
		t.Fatalf("bare URL alongside markup not linkified: %q", out)
	}
}

// A host-relative verify path recovered from a legacy rendered badge
// line must become absolute; the sanitizer drops OSC8 targets without
// an http(s) scheme, which would leave the verify link as plain text.
func TestAbsoluteVerifyURL(t *testing.T) {
	s := &Session{WSURL: "wss://chat.example.com/ws"}
	if got := s.absoluteVerifyURL("/api/trip/verify?pub=a"); got != "https://chat.example.com/api/trip/verify?pub=a" {
		t.Fatalf("relative path = %q", got)
	}
	if got := s.absoluteVerifyURL("https://other.example.com/api/trip/verify"); got != "https://other.example.com/api/trip/verify" {
		t.Fatalf("absolute URL must pass through, got %q", got)
	}
	// Unknown host: best effort, never crash or double-prefix.
	s2 := &Session{}
	if got := s2.absoluteVerifyURL("/api/trip/verify"); got != "/api/trip/verify" {
		t.Fatalf("empty host = %q", got)
	}
}

// The legacy badge path rebuilds the verify hyperlink; with the absolute
// URL the display sanitizer keeps the OSC8 target, so a click still
// opens the stateless API (previously the relative path was dropped).
func TestAbsoluteVerifyURLSurvivesSanitize(t *testing.T) {
	s := &Session{WSURL: "wss://chat.example.com/ws"}
	jobURL := s.absoluteVerifyURL("/api/trip/verify?pub=a&sig=b")
	if !strings.HasPrefix(jobURL, "https://chat.example.com/") {
		t.Fatalf("not absolute: %q", jobURL)
	}
	line := "|   └─ ✍️ \x1b]8;;" + jobURL + "\x1b\\\x1b[94m◆ abcd1234\x1b[0m\x1b]8;;\x1b\\"
	sanitized := filter.SanitizeForDisplay(line)
	if !strings.Contains(sanitized, "\x1b]8;;"+jobURL+"\x1b\\") {
		t.Fatalf("verify link stripped by sanitizer: %q", sanitized)
	}
}

// The badge verify link carries display-only context (chain height and
// send timestamp) so the verify page can line its fields up with /info.
func TestBadgeVerifyURLCarriesHeightAndSentAt(t *testing.T) {
	s := &Session{WSURL: "wss://chat.example.com/ws"}
	s.Chain.ServerPubHex = "00"
	wire := WireMessage{
		ChainHeight: 42,
		SentAt:      "2026-09-27T03:27:45+07:00",
		DisplayName: "Alice",
		Trip: &TripMeta{
			Pub: "aa", Seq: 7, Prev: "bb", Sig: "cc", MsgHash: "dd", ServerPub: "ee",
		},
	}
	_, urlStr := s.badgeForWire(wire, false)
	u, err := url.Parse(urlStr)
	if err != nil {
		t.Fatalf("verify url unparsable: %v (%q)", err, urlStr)
	}
	q := u.Query()
	if got := q.Get("height"); got != "42" {
		t.Fatalf("height param = %q, want 42", got)
	}
	if got := q.Get("sent_at"); got != "2026-09-27T03:27:45+07:00" {
		t.Fatalf("sent_at param = %q", got)
	}
	// The signed trip inputs must remain present and unrenamed.
	for _, k := range []string{"pub", "seq", "prev", "sig", "msg_hash", "server_pub"} {
		if q.Get(k) == "" {
			t.Fatalf("verify url lost signed param %q: %q", k, urlStr)
		}
	}
}

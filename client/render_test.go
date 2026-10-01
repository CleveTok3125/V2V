package main

import (
	"net/url"
	"strings"
	"testing"

	"github.com/CleveTok3125/V2V/internal/filter"
	"github.com/CleveTok3125/V2V/internal/wire"
)

// TestParseHistoryBoundary: the replay state machine, the fork check and
// echo matching all key off this, and it must read the marker's tags. A
// marker-shaped string with no history tag is not a marker, and a footer is
// recognised by its end/exhausted leaf rather than by a trailing " ---".
func TestParseHistoryBoundary(t *testing.T) {
	cases := []struct {
		name     string
		tags     []string
		boundary bool
		start    bool
	}{
		{"join header", wire.WithTags(wire.TagHistoryBegin), true, true},
		{"segment header", wire.WithTags(wire.TagHistoryOlder), true, true},
		{"recovery header", wire.WithTags(wire.TagHistoryRecover), true, true},
		{"counted footer", wire.WithTags(wire.TagHistory, wire.TagHistoryEnd), true, false},
		{"exhausted footer", wire.WithTags(wire.TagHistoryOlder, wire.TagHistoryExhausted), true, false},
		{"recovery footer", wire.WithTags(wire.TagHistoryRecover, wire.TagHistoryEnd), true, false},
		{"chat", nil, false, false},
		{"ordinary notice", wire.WithTags(wire.TagLimit), false, false},
	}
	for _, tc := range cases {
		gotBoundary, gotStart := parseHistoryBoundary(tc.tags)
		if gotBoundary != tc.boundary || gotStart != tc.start {
			t.Errorf("%s: parseHistoryBoundary = (%v,%v), want (%v,%v)",
				tc.name, gotBoundary, gotStart, tc.boundary, tc.start)
		}
	}
}

// Tab routing is decided by the wire's type and tags. The server tags a
// line at the source, so matching wording here would only be a second,
// drifting copy of that decision: a line whose text happens to look like a
// marker is still just a system line.
func TestTabForWire(t *testing.T) {
	cases := []struct {
		name string
		msg  WireMessage
		want int
	}{
		{"chat", WireMessage{Type: "chat", Text: "hi"}, TabChat},
		// A marker frames the chat stream, so it belongs beside it.
		{"history end", WireMessage{Type: "system", Tags: wire.WithTags(wire.TagHistoryEnd), Text: "--- Kết thúc lịch sử ---"}, TabChat},
		{"history begin", WireMessage{Type: "system", Tags: wire.WithTags(wire.TagHistoryBegin)}, TabChat},
		{"date", WireMessage{Type: "system", Tags: wire.WithTags(wire.TagDate)}, TabSystem},
		{"join", WireMessage{Type: "system", Tags: wire.WithTags(wire.TagJoin)}, TabSystem},
		{"limit", WireMessage{Type: "system", Tags: wire.WithTags(wire.TagLimit)}, TabSystem},
		{"screening", WireMessage{Type: "system", Tags: wire.WithTags(wire.TagPowScreen)}, TabSystem},
		// Marker-shaped text without the tag is an ordinary system line.
		{"marker wording untagged", WireMessage{Type: "system", Text: "--- Lịch sử chat gần đây ---"}, TabSystem},
		// Tagged system line with marker wording still goes to the tab its
		// tags name, not the tab its words suggest.
		{"screening with marker wording", WireMessage{Type: "system", Tags: wire.WithTags(wire.TagPowScreen), Text: "--- Kết thúc lịch sử ---"}, TabSystem},
	}
	for _, tc := range cases {
		if got := tabForWire(tc.msg); got != tc.want {
			t.Errorf("%s: tabForWire = %d, want %d", tc.name, got, tc.want)
		}
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

// TestHandleReplayMarkerTracksWindows: the marker drives the replay state
// machine, the fork check and the recovery refill. Each header must raise
// its own window and leave the others alone, any footer must clear all of
// them, and a recovery window must settle its refill on its own footer.
func TestHandleReplayMarkerTracksWindows(t *testing.T) {
	marker := func(tags ...string) WireMessage {
		return WireMessage{Type: "system", Tags: wire.WithTags(tags...), Text: "marker"}
	}

	cases := []struct {
		name    string
		open    []string
		inOlder bool
		inSync  bool
		inRecov bool
	}{
		{"join", []string{wire.TagHistoryBegin}, false, true, false},
		{"segment", []string{wire.TagHistoryOlder}, true, false, false},
		{"recovery", []string{wire.TagHistoryRecover}, false, false, true},
	}
	for _, tc := range cases {
		sess := sessionForDispatch(t)
		sess.handleReplayMarker(marker(tc.open...))
		if sess.Chain.InOlder != tc.inOlder || sess.Chain.InSync != tc.inSync || sess.Chain.InRecover != tc.inRecov {
			t.Errorf("%s header: InOlder %v InSync %v InRecover %v, want %v/%v/%v",
				tc.name, sess.Chain.InOlder, sess.Chain.InSync, sess.Chain.InRecover,
				tc.inOlder, tc.inSync, tc.inRecov)
		}
		// A counted footer closes whatever window was open.
		sess.handleReplayMarker(marker(wire.TagHistory, wire.TagHistoryEnd))
		if sess.Chain.InOlder || sess.Chain.InSync || sess.Chain.InRecover {
			t.Errorf("%s footer must clear every window, got %v/%v/%v",
				tc.name, sess.Chain.InOlder, sess.Chain.InSync, sess.Chain.InRecover)
		}
		// So does an exhausted one: it closes the window just as firmly.
		sess.handleReplayMarker(marker(wire.TagHistoryOlder))
		sess.handleReplayMarker(marker(wire.TagHistoryOlder, wire.TagHistoryExhausted))
		if sess.Chain.InOlder || sess.Chain.InSync || sess.Chain.InRecover {
			t.Errorf("%s exhausted footer must clear every window, got %v/%v/%v",
				tc.name, sess.Chain.InOlder, sess.Chain.InSync, sess.Chain.InRecover)
		}
	}
}

// TestHandleReplayMarkerHidesRecoveryAndLoading: markers are noise while a
// load is in progress, and a recovery window's markers are noise on their
// own because the merge draws its own summary.
func TestHandleReplayMarkerHidesRecoveryAndLoading(t *testing.T) {
	marker := func(tags ...string) WireMessage {
		return WireMessage{Type: "system", Tags: wire.WithTags(tags...), Text: "MARKERTEXT"}
	}

	sess := sessionForDispatch(t)
	sess.handleReplayMarker(marker(wire.TagHistoryOlder))
	if !containsLine(sess.Display.TabChat.lines, "MARKERTEXT") {
		t.Fatal("a plain segment marker must be drawn in the chat tab")
	}

	sess = sessionForDispatch(t)
	sess.handleReplayMarker(marker(wire.TagHistoryRecover))
	if containsLine(sess.Display.TabChat.lines, "MARKERTEXT") {
		t.Fatal("recovery markers must stay hidden")
	}

	sess = sessionForDispatch(t)
	sess.Chain.Loading = true
	sess.handleReplayMarker(marker(wire.TagHistoryBegin))
	if containsLine(sess.Display.TabChat.lines, "MARKERTEXT") {
		t.Fatal("markers must stay hidden while a load is in progress")
	}
}

// TestHandleReplayMarkerIgnoresWording: only the tags decide. A marker with
// no history tag is an ordinary notice and must not move the state machine.
func TestHandleReplayMarkerIgnoresWording(t *testing.T) {
	sess := sessionForDispatch(t)
	sess.handleReplayMarker(WireMessage{Type: "system", Text: "--- Lịch sử cũ ---"})
	if sess.Chain.InOlder || sess.Chain.InSync || sess.Chain.InRecover {
		t.Fatal("an untagged marker-shaped line must not open a replay window")
	}
}

func containsLine(lines []string, needle string) bool {
	for _, line := range lines {
		if strings.Contains(line, needle) {
			return true
		}
	}
	return false
}

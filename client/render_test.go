package main

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"net/url"
	"strings"
	"testing"

	"github.com/CleveTok3125/V2V/lib/filter"
	"github.com/CleveTok3125/V2V/lib/tripcolor"
	"github.com/CleveTok3125/V2V/lib/wire"
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
		// Every system line belongs to TabSystem, markers included, so tab 2
		// stays a complete system log.
		{"history end", WireMessage{Type: "system", Tags: wire.WithTags(wire.TagHistoryEnd), Text: "--- Kết thúc lịch sử ---"}, TabSystem},
		{"history begin", WireMessage{Type: "system", Tags: wire.WithTags(wire.TagHistoryBegin)}, TabSystem},
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

// A revealed identity keeps a valid signature but loses trust: the badge
// renders with an exposure marker instead of the tamper cross.
func TestExposedBadgeRendersMarker(t *testing.T) {
	seed := make([]byte, 32)
	for i := range seed {
		seed[i] = byte(i + 13)
	}
	priv := ed25519.NewKeyFromSeed(seed)
	pub := priv.Public().(ed25519.PublicKey)
	prev := make([]byte, 32)
	msgHash := sha256.Sum256([]byte("hello"))
	payload := tripcolor.CanonicalPayload("", 1, prev, msgHash[:], pub, "Bob#1234", 7, 0)
	sig := ed25519.Sign(priv, payload)
	s := &Session{WSURL: "wss://chat.example.com/ws"}
	wire := WireMessage{
		Text: "hello", DisplayName: "Bob#1234",
		Trip: &TripMeta{
			Pub: hex.EncodeToString(pub), Seq: 1,
			Prev: hex.EncodeToString(prev), Sig: hex.EncodeToString(sig),
			ServerPub: "", MsgHash: hex.EncodeToString(msgHash[:]),
			DisplayName: "Bob#1234", TmpID: 7, Exposed: true,
		},
	}
	colored, _ := s.badgeForWire(wire, true)
	if !strings.Contains(colored, "(đã lộ)") {
		t.Fatalf("exposed badge must mark: %q", colored)
	}
	if strings.Contains(colored, "✗") {
		t.Fatalf("exposed valid sig must not cross: %q", colored)
	}
	unverifiedExposed, _ := s.badgeForWire(wire, false)
	if !strings.Contains(unverifiedExposed, "(đã lộ)") {
		t.Fatalf("exposed badge with autoverify off must still mark: %q", unverifiedExposed)
	}
	wire.Trip.Exposed = false
	plain, _ := s.badgeForWire(wire, true)
	if strings.Contains(plain, "(đã lộ)") {
		t.Fatalf("clean badge must not mark: %q", plain)
	}
	unverifiedClean, _ := s.badgeForWire(wire, false)
	if strings.Contains(unverifiedClean, "(đã lộ)") {
		t.Fatalf("clean badge with autoverify off must not mark: %q", unverifiedClean)
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

// TestHandleReplayMarkerHidesAll: markers from older servers never render
// as chat lines. Progress lives in the status bar from the trailer.
func TestHandleReplayMarkerHidesAll(t *testing.T) {
	marker := func(tags ...string) WireMessage {
		return WireMessage{Type: "system", Tags: wire.WithTags(tags...), Text: "MARKERTEXT"}
	}

	for _, tags := range [][]string{
		{wire.TagHistoryOlder},
		{wire.TagHistoryRecover},
		{wire.TagHistoryBegin},
	} {
		sess := sessionForDispatch(t)
		sess.handleReplayMarker(marker(tags...))
		if containsLine(sess.Display.TabChat.lines, "MARKERTEXT") {
			t.Fatalf("markers must never draw in chat: %v", tags)
		}
		if containsLine(sess.Display.TabSys.lines, "MARKERTEXT") {
			t.Fatalf("markers must never land in system tab: %v", tags)
		}
	}

	sess := sessionForDispatch(t)
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

func containsLine(lines []entry, needle string) bool {
	for _, line := range lines {
		if strings.Contains(line.text(), needle) {
			return true
		}
	}
	return false
}

// TestHandleReplayMarkerNeverRenders: markers only drive the replay state
// machine for older servers. They never print, so notify gates do not
// apply to them.
func TestHandleReplayMarkerNeverRenders(t *testing.T) {
	marker := func(tags ...string) WireMessage {
		return WireMessage{Type: "system", Tags: wire.WithTags(tags...), Text: "MARKERTEXT"}
	}

	for _, muted := range []string{wire.TagHistory, wire.TagRoot, wire.TagJoin} {
		sess, out := queueTestSession(t)
		sess.Chain.WireIdx = newWireIndex(8)
		sess.Chain.RenderCache = newRenderCache(8)
		sess.Display.Notify = NotifyState{Muted: map[string]bool{muted: true}, PowMinTier: 1}
		sess.handleReplayMarker(marker(wire.TagHistoryOlder))

		if strings.Contains(out.String(), "MARKERTEXT") {
			t.Errorf("muting %q must not print a marker", muted)
		}
		if containsLine(sess.Display.TabSys.lines, "MARKERTEXT") {
			t.Errorf("muting %q must not store a marker", muted)
		}
	}
}

// TestChatBlockShape pins what a rendered block looks like: the author and
// chain position share one header row, the body hangs below it under a
// shared indent, and a notice is a single row. Every entry ends with the
// blank row that separates it from the next, so the spacing travels with the
// entry through a repaint, a slot replacement and the erase math alike.
func TestChatBlockShape(t *testing.T) {
	sess := chainTestSession(t, 0)

	chat := WireMessage{Type: "chat", Time: "14:19", DisplayName: "Alice#a11c", Text: "hi"}
	e, _ := sess.buildChatBlock(chat, false, false)
	head := e.text()
	if !strings.HasSuffix(head, "\n\n") || strings.HasSuffix(head, "\n\n\n") {
		t.Fatalf("a chat line must end in exactly one blank row, got %q", head)
	}
	if !strings.Contains(head, "\nhi") {
		t.Fatalf("a chat body must ride below the header, got %q", head)
	}
	if !strings.HasPrefix(head, "14:19 Alice#a11c") {
		t.Fatalf("the author row must lead, got %q", head)
	}

	sys := WireMessage{Type: "system", Time: "14:19", Tags: []string{"system", wire.TagJoin}, Text: "ai đó đã tham gia"}
	e, _ = sess.buildChatBlock(sys, false, false)
	if head := e.text(); head != "ai đó đã tham gia\n\n" {
		t.Fatalf("a notice is one row plus the gap, got %q", head)
	}
}

// TestOneMessageIsOneEntry pins the shape the rest of the display rests on: a
// message is a single buffer entry whatever it carries, quote included. That
// is what keeps the erase math, the catch-up anchor and the quote lookup
// agreeing on one unit, and it is why nothing can land between a message's
// parts.
func TestOneMessageIsOneEntry(t *testing.T) {
	sess := chainTestSession(t, 0)
	sess.Chain.WireIdx = newWireIndex(1000)
	sess.Chain.RenderCache = newRenderCache(200)
	sess.Display.ActiveTab = TabChat
	sess.Display.Term = &fakeTerm{}

	first := chainWireWithText(1, "nội dung gốc", 1)
	reply := chainWireWithText(2, "trả lời đây", 2)
	reply.ReplyTo = 1

	sess.Display.DisplayMu.Lock()
	sess.renderChatBlock(first)
	sess.renderChatBlock(reply)
	sess.Display.DisplayMu.Unlock()

	if got := len(sess.Display.TabChat.lines); got != 2 {
		t.Fatalf("two messages must be two entries, got %d: %q", got, entriesText(sess.Display.TabChat.lines))
	}
	// Literal, not the constant: a test that follows the code cannot catch a
	// change to the marker it is pinning.
	e := sess.Display.TabChat.lines[1]
	if !strings.HasPrefix(e.quote, "↩") {
		t.Fatalf("the quote must ride inside its message entry, got %q", e.quote)
	}
	if e.header != "12:00 A" {
		t.Fatalf("the header must follow the quote in the same entry, got %q", e.header)
	}
	// The quote lookup reads the body field out of that entry, skipping the
	// quote row and the header.
	if e.body != "trả lời đây" {
		t.Fatalf("entry body = %q, want the body alone", e.body)
	}
	// And the painted chat carries the same two units, in order.
	chat := &chatSpy{sess: sess}
	if got := strings.Count(chat.String(), "↩"); got != 1 {
		t.Fatalf("the quote must be printed once, got %d in %q", got, chat.String())
	}
}

// TestSwitchTabPaintsEverythingBeforeThePrompt: a switch must leave nothing
// queued. The bar used to be emitted on its own, so it sat on the queue while
// readline was already drawing the prompt and landed on the input line; the
// clear, the dump and the bar are now one ordered chunk.

func TestLoneFenceRendersLiteral(t *testing.T) {
	// A fence with nothing in it is text, not an empty block.
	if got := stripANSIForFind(renderChatText("```")); got != "```" {
		t.Fatalf("lone fence = %q, want the literal text", got)
	}
	// A closed fence with nothing between the markers renders nothing, so the
	// body falls back to the raw text.
	sess, _ := queueTestSession(t)
	sess.Chain.WireIdx = newWireIndex(4)
	e, _ := sess.buildChatBlock(WireMessage{Type: "chat", Time: "15:04", DisplayName: "A", Text: "```\n```"}, false, false)
	if got := stripANSIForFind(e.body); !strings.Contains(got, "```") {
		t.Fatalf("empty body = %q, want the raw text", got)
	}
}

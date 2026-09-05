package main

import (
	"encoding/hex"
	"strings"
	"testing"
	"time"

	"github.com/CleveTok3125/V2V/internal/chain"
)

func TestMetaLineFor(t *testing.T) {
	got := metaLineFor(1234, "abcdef0123456789", "")
	if got != "  └─  #1234:abcd" {
		t.Fatalf("got %q", got)
	}
	got = metaLineFor(7, "ABCDEF", "◆ ab12")
	if got != "  └─  #7:abcd | ✍️ ◆ ab12" {
		t.Fatalf("badge must append after separator, got %q", got)
	}
}

func TestMatchPendingIndex(t *testing.T) {
	pending := []pendingMsg{
		{text: "same", tmpID: 1, sentAt: time.Now()},
		{text: "same", tmpID: 2, sentAt: time.Now()},
	}
	// Exact tmp_id wins even with duplicate texts.
	if i := matchPendingIndex(pending, 2, 0, "same", "me", "me"); i != 1 {
		t.Fatalf("want 1, got %d", i)
	}
	// Legacy oldest-text fallback when the echo carries no ID.
	if i := matchPendingIndex(pending, 0, 0, "same", "me", "me"); i != 0 {
		t.Fatalf("want 0, got %d", i)
	}
	// Another user's echo never matches our placeholders.
	if i := matchPendingIndex(pending, 2, 0, "same", "me", "you"); i != -1 {
		t.Fatalf("want -1, got %d", i)
	}
	// Unknown ID with different text falls back to no match (texts differ).
	if i := matchPendingIndex(pending, 9, 0, "other", "me", "me"); i != -1 {
		t.Fatalf("want -1, got %d", i)
	}
	if i := matchPendingIndex(nil, 1, 0, "same", "me", "me"); i != -1 {
		t.Fatalf("want -1, got %d", i)
	}
}

func TestChainTipRoundtrip(t *testing.T) {
	path := chainTipFile("")
	if !strings.HasSuffix(path, "chain_tip.json") {
		t.Fatalf("unexpected tip path %q", path)
	}
	dir := t.TempDir()
	p := dir + "/chain_tip.json"
	var tip [32]byte
	tip[0] = 0x42
	saveChainTip(p, tip, 12, "srvpub")
	got, height, srv, ok := loadChainTip(p)
	if !ok || got != tip || height != 12 || srv != "srvpub" {
		t.Fatalf("roundtrip failed: %v %d %q %v", got, height, srv, ok)
	}
	if _, _, _, ok := loadChainTip(dir + "/missing.json"); ok {
		t.Fatal("missing file must not load")
	}
	// Foreign-server tips load but must be ignored by the caller.
	if _, _, srv, ok := loadChainTip(p); !ok || srv == "other" {
		t.Fatalf("server_pub must roundtrip, got %q %v", srv, ok)
	}
}

func chainedTestWire(prev [32]byte, height, tmpID uint64) WireMessage {	h := chain.Hash(prev, height, tmpID, 0, "chat", "15:04", "Alice", "hello", "")
	return WireMessage{
		Type: "chat", Time: "15:04", DisplayName: "Alice", Text: "hello",
		TmpID:       tmpID,
		ChainPrev:   hex.EncodeToString(prev[:]),
		ChainHash:   hex.EncodeToString(h[:]),
		ChainHeight: height,
		ChainVer:    2,
	}
}

func TestVerifyWireLink(t *testing.T) {
	genesis := chain.Genesis("srv")
	wire := chainedTestWire(genesis, 1, 5)
	tip, err := verifyWireLink(wire, genesis)
	if err != nil {
		t.Fatalf("valid link must verify: %v", err)
	}
	want, _ := chain.ParseHex64(wire.ChainHash)
	if tip != want {
		t.Fatal("tip must advance to the message hash")
	}
	// Content tampering breaks the link.
	bad := wire
	bad.Text = "edited"
	if _, err := verifyWireLink(bad, genesis); err == nil {
		t.Fatal("tampered content must fail")
	}
	// Wrong continuity breaks the link.
	var other [32]byte
	other[0] = 1
	if _, err := verifyWireLink(wire, other); err == nil {
		t.Fatal("wrong prev must fail")
	}
}

func TestStashEchoRoundtrip(t *testing.T) {
	var stash []pendingEcho
	mk := func(id uint64) WireMessage { return WireMessage{TmpID: id, Text: "x"} }
	stash = stashEcho(stash, mk(1), 16)
	stash = stashEcho(stash, mk(2), 16)
	var got WireMessage
	var ok bool
	stash, got, ok = takeStashedEcho(stash, 2)
	if !ok || got.TmpID != 2 || len(stash) != 1 {
		t.Fatalf("take must remove exactly the match: %v %d", ok, len(stash))
	}
	if _, _, ok = takeStashedEcho(stash, 9); ok {
		t.Fatal("unknown tmp_id must miss")
	}
	// Bound respected.
	for i := uint64(0); i < 20; i++ {
		stash = stashEcho(stash, mk(i), 4)
	}
	if len(stash) != 4 {
		t.Fatalf("stash must stay bounded, got %d", len(stash))
	}
}

func TestReapStaleEchoes(t *testing.T) {
	stash := []pendingEcho{
		{wire: WireMessage{TmpID: 1}, at: time.Now().Add(-time.Hour)},
		{wire: WireMessage{TmpID: 2}, at: time.Now()},
	}
	kept, stale := reapStaleEchoes(stash, 10*time.Second)
	if len(kept) != 1 || len(stale) != 1 || stale[0].TmpID != 1 {
		t.Fatalf("got kept=%d stale=%d", len(kept), len(stale))
	}
}

func TestParseFindArg(t *testing.T) {
	cases := []struct {
		in        string
		height    uint64
		suffix    string
		wantErr   string
	}{
		{"1234", 1234, "", ""},
		{"#1234", 1234, "", ""},
		{"  42:abcd  ", 42, "abcd", ""},
		{"#7:AB12", 7, "ab12", ""},
		{"", 0, "", "usage"},
		{"#", 0, "", "usage"},
		{"0", 0, "", "usage"},
		{"abc", 0, "", "usage"},
		{"abcd", 0, "", "usage"},
		{":abcd", 0, "", "bare hash"},
		{"#:abcd", 0, "", "bare hash"},
		{"12:zz", 0, "", "hex"},
	}
	for _, c := range cases {
		h, s, err := parseFindArg(c.in)
		if c.wantErr != "" {
			if err == nil || !strings.Contains(err.Error(), c.wantErr) {
				t.Errorf("parseFindArg(%q) err = %v, want containing %q", c.in, err, c.wantErr)
			}
			continue
		}
		if err != nil || h != c.height || s != c.suffix {
			t.Errorf("parseFindArg(%q) = %d %q %v", c.in, h, s, err)
		}
	}
}

func TestFindMetaMatches(t *testing.T) {
	lines := []string{
		"| 15:04 Alice: hello\n",
		"|   └─  #1234:abcd\n",
		"| 15:05 Bob: quoted \"└─  #1234:abcd\" text\n",
		"|   └─  #1235:abce | ✍️ \x1b[38;2;1;2;3m◆ x9y8\x1b[0m\n",
		"| [Local]: something\n",
	}
	// Height-only matches the real meta (and the quoted content line,
	// which is indistinguishable post-render and listed too).
	got := findMetaMatches(lines, 1234, "")
	if len(got) != 2 || got[0] != 1 || got[1] != 2 {
		t.Fatalf("got %v", got)
	}
	// Suffix checksum narrows to the true meta.
	got = findMetaMatches(lines, 1235, "abce")
	if len(got) != 1 || got[0] != 3 {
		t.Fatalf("got %v", got)
	}
	// Wrong suffix kills the match (typo guard).
	if got := findMetaMatches(lines, 1235, "ffff"); len(got) != 0 {
		t.Fatalf("got %v", got)
	}
	if got := findMetaMatches(lines, 9999, ""); len(got) != 0 {
		t.Fatalf("got %v", got)
	}
}

func TestRenderCacheBounded(t *testing.T) {
	c := newRenderCache(3)
	mk := func(tab int) renderedBlock { return renderedBlock{tab: tab, head: "h", meta: "m", hasMeta: true} }
	c.put("a", mk(1))
	c.put("b", mk(1))
	c.put("c", mk(1))
	if _, ok := c.get("a"); !ok {
		t.Fatal("a must hit")
	}
	c.put("d", mk(2))
	if _, ok := c.get("a"); ok {
		t.Fatal("a must evict FIFO")
	}
	if b, ok := c.get("d"); !ok || b.tab != 2 {
		t.Fatal("d must hit with tab 2")
	}
	// Re-put refreshes value without duplicating order.
	c.put("b", mk(2))
	c.put("e", mk(1))
	c.put("f", mk(1))
	if _, ok := c.get("b"); ok {
		t.Fatal("b must evict after order advanced")
	}
}

func TestFindMentions(t *testing.T) {
	ms := findMentions("see @#1234 and @#56:abcd ok")
	if len(ms) != 2 || ms[0].height != 1234 || ms[1].height != 56 || ms[1].suffix != "abcd" {
		t.Fatalf("got %+v", ms)
	}
	for _, s := range []string{
		"mail a@#1234x",   // trailing ident char
		"x@#1234",         // Wait: @ preceded by ident char -> skip
		"@#0",             // zero height
		"@#12:xyz",        // non-hex tail voids
		"name#123 ok",     // no @ prefix
		"@#12#34",         // trailing # voids
	} {
		_ = s
	}
	if got := findMentions("mail a@#1234x"); len(got) != 0 {
		t.Fatalf("trailing ident must void: %+v", got)
	}
	if got := findMentions("x@#1234"); len(got) != 0 {
		t.Fatalf("leading ident must void: %+v", got)
	}
	if got := findMentions("@#0"); len(got) != 0 {
		t.Fatalf("zero height must void: %+v", got)
	}
	if got := findMentions("@#12:xyz"); len(got) != 0 {
		t.Fatalf("non-hex tail must void: %+v", got)
	}
	if got := findMentions("name#123 ok"); len(got) != 0 {
		t.Fatalf("missing @ must void: %+v", got)
	}
}

func TestRenderMentions(t *testing.T) {
	resolve := func(h uint64, s string) bool { return h == 7 && (s == "" || s == "ab") }
	open, close := mentionSGR([3]int{0, 255, 255})
	if open != "\x1b[1;96m" || close != "\x1b[22;39m" {
		t.Fatalf("default must stay 16-color, got %q %q", open, close)
	}
	open, close = mentionSGR([3]int{255, 0, 0})
	if open != "\x1b[1;38;2;255;0;0m" {
		t.Fatalf("custom must be truecolor, got %q", open)
	}
	got := renderMentions("see @#7 and @#8 and @#7:zz", resolve, true, open, close)
	if !strings.Contains(got, "\x1b[1;38;2;255;0;0m@#7\x1b[22;39m") {
		t.Fatalf("resolved mention must highlight: %q", got)
	}
	if strings.Contains(got, "@#8\x1b") || strings.Contains(got, "@#7:zz\x1b") {
		t.Fatalf("unresolved/suffix-mismatch must stay plain: %q", got)
	}
	// Disabled renders everything plain.
	if got := renderMentions("see @#7", resolve, false, open, close); got != "see @#7" {
		t.Fatalf("disabled must stay plain: %q", got)
	}
	// SGR spans are never entered.
	in := "\x1b[48;5;236m@#7\x1b[49m tail @#7"
	got = renderMentions(in, resolve, true, open, close)
	if strings.Contains(got, "\x1b[48;5;236m\x1b[1;") {
		t.Fatalf("must not highlight inside SGR span: %q", got)
	}
	if !strings.Contains(got, "\x1b[1;38;2;255;0;0m@#7\x1b[22;39m") {
		t.Fatalf("plain mention must still highlight: %q", got)
	}
}

func TestMatchPendingReplyTo(t *testing.T) {
	pending := []pendingMsg{
		{text: "ans", tmpID: 1, replyTo: 5, sentAt: time.Now()},
	}
	if i := matchPendingIndex(pending, 1, 5, "ans", "me", "me"); i != 0 {
		t.Fatalf("want 0, got %d", i)
	}
	// Server swapping the quote target must not resolve the placeholder.
	if i := matchPendingIndex(pending, 1, 6, "ans", "me", "me"); i != -1 {
		t.Fatalf("swapped reply_to must miss, got %d", i)
	}
}

func TestFormatQuote(t *testing.T) {
	got := formatQuote(12, "| 15:04 Alice: hello world\n", false, 80)
	if got != "| ↩ #12: 15:04 Alice: hello world" {
		t.Fatalf("got %q", got)
	}
	got = formatQuote(12, "\x1b[90m| Bạn: x ⏳\x1b[0m\n", true, 80)
	if !strings.HasPrefix(got, "\x1b[90m| ↩ #12:") || !strings.HasSuffix(got, "⏳\x1b[0m") {
		t.Fatalf("pending quote must be grey with hourglass: %q", got)
	}
	long := "| X: " + strings.Repeat("a", 200)
	got = formatQuote(3, long, false, 80)
	if strings.Count(got, "a") > 80 || !strings.HasSuffix(got, "…") {
		t.Fatalf("must truncate: %q", got)
	}
	got = formatQuote(3, long, false, 30)
	if strings.Count(got, "a") > 30 {
		t.Fatalf("must honor custom max: %q", got)
	}
	got = formatQuote(3, long, false, 0)
	if strings.Count(got, "a") > 20 {
		t.Fatalf("must clamp tiny max: %q", got)
	}
}

func TestWantsMeta(t *testing.T) {
	chained := WireMessage{Type: "chat", ChainHash: "ab12", ChainHeight: 3}
	if !wantsMeta(chained, true) {
		t.Fatal("chained chat must earn meta")
	}
	if wantsMeta(chained, false) {
		t.Fatal("/meta off must hide meta")
	}
	sys := WireMessage{Type: "system", Text: "--- Ngày x ---", ChainHash: "ab12", ChainHeight: 1}
	if wantsMeta(sys, true) {
		t.Fatal("server notices must not render meta")
	}
	legacy := WireMessage{Type: "chat", Text: "old"}
	if wantsMeta(legacy, true) {
		t.Fatal("legacy lines must not render meta")
	}
}

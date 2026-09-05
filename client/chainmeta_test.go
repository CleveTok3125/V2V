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
	if i := matchPendingIndex(pending, 2, "same", "me", "me"); i != 1 {
		t.Fatalf("want 1, got %d", i)
	}
	// Legacy oldest-text fallback when the echo carries no ID.
	if i := matchPendingIndex(pending, 0, "same", "me", "me"); i != 0 {
		t.Fatalf("want 0, got %d", i)
	}
	// Another user's echo never matches our placeholders.
	if i := matchPendingIndex(pending, 2, "same", "me", "you"); i != -1 {
		t.Fatalf("want -1, got %d", i)
	}
	// Unknown ID with different text falls back to no match (texts differ).
	if i := matchPendingIndex(pending, 9, "other", "me", "me"); i != -1 {
		t.Fatalf("want -1, got %d", i)
	}
	if i := matchPendingIndex(nil, 1, "same", "me", "me"); i != -1 {
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
	saveChainTip(p, tip, 12)
	got, height, ok := loadChainTip(p)
	if !ok || got != tip || height != 12 {
		t.Fatalf("roundtrip failed: %v %d %v", got, height, ok)
	}
	if _, _, ok := loadChainTip(dir + "/missing.json"); ok {
		t.Fatal("missing file must not load")
	}
}

func chainedTestWire(prev [32]byte, height, tmpID uint64) WireMessage {	h := chain.Hash(prev, height, tmpID, "chat", "15:04", "Alice", "hello", "")
	return WireMessage{
		Type: "chat", Time: "15:04", DisplayName: "Alice", Text: "hello",
		TmpID:       tmpID,
		ChainPrev:   hex.EncodeToString(prev[:]),
		ChainHash:   hex.EncodeToString(h[:]),
		ChainHeight: height,
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

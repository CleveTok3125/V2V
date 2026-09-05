package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/CleveTok3125/V2V/internal/chain"
	"github.com/CleveTok3125/V2V/internal/filter"
	"github.com/CleveTok3125/V2V/internal/trip"
	"github.com/CleveTok3125/V2V/internal/tripcolor"
)

// Chain meta line and echo matching helpers. Pure logic lives here for
// tests; terminal/connection state stays in client.go.

// pendingMsg tracks a grey placeholder awaiting server echo.
type pendingMsg struct {
	text    string
	rows    int
	shown   bool
	gen     uint64
	bufEnd  int
	seq     uint32
	pub     string
	hasTrip bool
	sentAt  time.Time
	tmpID   uint64
	replyTo uint64
}

// metaLineFor builds the trailing metadata line of a message block:
// "  └─  #height:hash4", with " | ✍️ badge" appended for trip messages.
// badgePart is the already-colored badge (with hyperlink when available).
func metaLineFor(height uint64, hashHex, badgePart string) string {
	var sb strings.Builder
	sb.WriteString("  └─  #")
	sb.WriteString(strconv.FormatUint(height, 10))
	sb.WriteString(":")
	if len(hashHex) >= 4 {
		sb.WriteString(strings.ToLower(hashHex[:4]))
	} else {
		sb.WriteString(hashHex)
	}
	if badgePart != "" {
		sb.WriteString(" | ✍️ ")
		sb.WriteString(badgePart)
	}
	return sb.String()
}

// matchPendingIndex finds the pending placeholder confirmed by a server
// echo. An exact tmp_id match wins (duplicate texts stay unambiguous);
// the quoted target must agree too, so server-side reply swapping leaves
// the placeholder visibly pending. Otherwise the oldest text match within
// a minute applies (pre-chain servers). It returns -1 when nothing matches.
func matchPendingIndex(pending []pendingMsg, tmpID uint64, replyTo uint64, text, username, echoDisplay string) int {
	if echoDisplay != username || len(pending) == 0 {
		return -1
	}
	if tmpID != 0 {
		for i, pm := range pending {
			if pm.tmpID == tmpID && pm.replyTo == replyTo {
				return i
			}
		}
		// An ID-carrying echo that matches nothing is either a server
		// rewrite or a race (handled via stash upstream) — never fall
		// back to text, or swapped IDs would resolve anyway.
		return -1
	}
	for i, pm := range pending {
		if pm.text == text && time.Since(pm.sentAt) < time.Minute {
			return i
		}
	}
	return -1
}

// pendingEcho holds a server echo of our own message that arrived before
// its placeholder was tracked (local servers echo in sub-milliseconds).
type pendingEcho struct {
	wire WireMessage
	at   time.Time
}

// stashEcho buffers an early echo, bounding the buffer.
func stashEcho(stash []pendingEcho, wire WireMessage, capN int) []pendingEcho {
	stash = append(stash, pendingEcho{wire: wire, at: time.Now()})
	for len(stash) > capN {
		stash = stash[1:]
	}
	return stash
}

// takeStashedEcho removes and returns the stashed echo with a matching
// tmp_id, if any.
func takeStashedEcho(stash []pendingEcho, tmpID uint64) ([]pendingEcho, WireMessage, bool) {
	for i, e := range stash {
		if e.wire.TmpID == tmpID {
			return append(stash[:i], stash[i+1:]...), e.wire, true
		}
	}
	return stash, WireMessage{}, false
}

// reapStaleEchoes drops entries older than maxAge and returns them so the
// caller can warn: an echo from us that never matched a placeholder means
// the server may have altered its ID.
func reapStaleEchoes(stash []pendingEcho, maxAge time.Duration) (kept []pendingEcho, stale []WireMessage) {
	now := time.Now()
	for _, e := range stash {
		if now.Sub(e.at) > maxAge {
			stale = append(stale, e.wire)
			continue
		}
		kept = append(kept, e)
	}
	return kept, stale
}

// renderedBlock is one cached message block: content head plus the
// trailing meta line, with the tab it belongs to.
type renderedBlock struct {
	tab     int
	head    string
	meta    string
	hasMeta bool
}

// renderCache is a bounded FIFO cache of rendered blocks keyed by chain
// hash (+verify mode). Chain links are content-bound and immutable, so a
// cached block never goes stale; palette changes need a restart anyway.
type renderCache struct {
	cap   int
	order []string
	items map[string]renderedBlock
}

func newRenderCache(capN int) *renderCache {
	if capN <= 0 {
		capN = 200
	}
	return &renderCache{cap: capN, items: make(map[string]renderedBlock)}
}

func (c *renderCache) get(key string) (renderedBlock, bool) {
	b, ok := c.items[key]
	return b, ok
}

func (c *renderCache) put(key string, b renderedBlock) {
	if _, ok := c.items[key]; !ok {
		c.order = append(c.order, key)
	}
	c.items[key] = b
	for len(c.order) > c.cap {
		delete(c.items, c.order[0])
		c.order = c.order[1:]
	}
}

// findMetaRe matches rendered meta lines ("└─  #1234:abcd"); the hash
// part may be empty when scanning, and is compared separately.
var findMetaRe = regexp.MustCompile(`└─\s+#(\d+):([0-9a-fA-F]*)`)

// mentionRe matches @-mentions of message heights ("@#1234", "@#1234:abcd").
// The @ prefix keeps them distinct from server-stamped "name#hash"
// display names, which never open with "@#".
var mentionRe = regexp.MustCompile(`@#(\d+)(?::([0-9a-fA-F]{1,16}))?`)

// sgrMention wraps a resolved mention; the closer resets bold + foreground
// so surrounding colors resume. Mentions only render in incoming/history
// content (default foreground), never in placeholders or meta lines.
const (
	sgrMentionOpen  = "\x1b[1;96m"
	sgrMentionClose = "\x1b[22;39m"
)

// mention is one @#height reference with byte offsets into its source.
type mention struct {
	start, end int
	height     uint64
	suffix     string
}

func isMentionBoundary(r rune) bool {
	switch {
	case r >= '0' && r <= '9', r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r == '_', r == '#', r == '@':
		return false
	}
	return true
}

// findMentions locates @#height references outside identifier/URL-like runs.
// A trailing colon not followed by hex voids the match (half-written suffix).
func findMentions(s string) []mention {
	var out []mention
	for _, loc := range mentionRe.FindAllStringSubmatchIndex(s, -1) {
		fullStart, fullEnd := loc[0], loc[1]
		if fullStart > 0 {
			if r, _ := utf8.DecodeLastRuneInString(s[:fullStart]); r != utf8.RuneError && !isMentionBoundary(r) {
				continue
			}
		}
		if fullEnd < len(s) {
			if r, _ := utf8.DecodeRuneInString(s[fullEnd:]); r != utf8.RuneError && !isMentionBoundary(r) {
				continue
			}
			// "@#1234:xyz" (non-hex tail): the regex stops before the
			// colon, so a bare colon right after means don't half-match.
			if s[fullEnd] == ':' {
				continue
			}
		}
		height, err := strconv.ParseUint(s[loc[2]:loc[3]], 10, 64)
		if err != nil || height == 0 {
			continue
		}
		suffix := ""
		if loc[4] >= 0 {
			suffix = strings.ToLower(s[loc[4]:loc[5]])
		}
		out = append(out, mention{start: fullStart, end: fullEnd, height: height, suffix: suffix})
	}
	return out
}

// ansiSpanRe matches terminal spans wholesale: an SGR-colored region
// (opener through its closer), an OSC8 hyperlink (markers plus linked
// text), or a lone escape. Mention scanning only sees the gaps, so code
// spans, links and badges are never entered.
var ansiSpanRe = regexp.MustCompile("\x1b\\[[0-9;]*m[\\s\\S]*?\x1b\\[[0-9;]*m|\x1b\\]8;;[^\x1b]*\x1b\\\\[\\s\\S]*?\x1b\\]8;;\x1b\\\\|\x1b\\[[0-9;]*m|\x1b\\]8;;[^\x1b]*\x1b\\\\")

// renderMentions highlights @#height references whose target resolves via
// resolve (height in buffer, suffix matching). Unresolved mentions stay
// plain text so evicted targets never mislead. Disabled renders plain.
func renderMentions(s string, resolve func(height uint64, suffix string) bool, enabled bool, open, close string) string {
	if !enabled || !strings.Contains(s, "@#") {
		return s
	}
	var sb strings.Builder
	pos := 0
	for _, loc := range ansiSpanRe.FindAllStringIndex(s, -1) {
		sb.WriteString(renderMentionsPlain(s[pos:loc[0]], resolve, open, close))
		sb.WriteString(s[loc[0]:loc[1]])
		pos = loc[1]
	}
	sb.WriteString(renderMentionsPlain(s[pos:], resolve, open, close))
	return sb.String()
}

func renderMentionsPlain(s string, resolve func(height uint64, suffix string) bool, open, close string) string {
	ms := findMentions(s)
	if len(ms) == 0 {
		return s
	}
	var sb strings.Builder
	pos := 0
	for _, m := range ms {
		if !resolve(m.height, m.suffix) {
			continue
		}
		sb.WriteString(s[pos:m.start])
		sb.WriteString(open)
		sb.WriteString(s[m.start:m.end])
		sb.WriteString(close)
		pos = m.end
	}
	sb.WriteString(s[pos:])
	return sb.String()
}

// mentionSGR builds the highlight pair from a clamped [r,g,b] triple with
// the bold attribute the default look carries. The default cyan emits the
// legacy 16-color sequence, which every terminal renders; custom colors
// use truecolor (needs truecolor support). The closer resets bold +
// foreground so surrounding colors resume.
func mentionSGR(c [3]int) (open, close string) {
	if c == ([3]int{0, 255, 255}) {
		return "\x1b[1;96m", sgrMentionClose
	}
	return fmt.Sprintf("\x1b[1;38;2;%d;%d;%dm", c[0], c[1], c[2]), sgrMentionClose
}

var ansiStripRe = regexp.MustCompile("\x1b\\[[0-9;]*m|\x1b\\]8;;[^\x1b]*\x1b\\\\")

// stripANSIForFind removes SGR/OSC8 sequences for text matching.
func stripANSIForFind(s string) string {
	return ansiStripRe.ReplaceAllString(s, "")
}

// parseFindArg parses "/find" arguments: "<height>" or "<height>:<hash>"
// with an optional leading "#". A bare hash without height is rejected:
// short hashes collide by design, only the height identifies.
func parseFindArg(arg string) (uint64, string, error) {
	t := strings.TrimSpace(arg)
	t = strings.TrimPrefix(t, "#")
	if t == "" {
		return 0, "", errors.New("usage: /find <height> | /find <height>:<hash>")
	}
	heightStr := t
	suffix := ""
	if i := strings.Index(t, ":"); i >= 0 {
		heightStr, suffix = t[:i], t[i+1:]
	}
	if heightStr == "" {
		return 0, "", errors.New("a bare hash never identifies: give the height (/find 1234[:abcd])")
	}
	height, err := strconv.ParseUint(heightStr, 10, 64)
	if err != nil || height == 0 {
		return 0, "", errors.New("usage: /find <height> | /find <height>:<hash>")
	}
	for _, r := range suffix {
		if !strings.ContainsRune("0123456789abcdefABCDEF", r) {
			return 0, "", errors.New("hash suffix must be hex")
		}
	}
	return height, strings.ToLower(suffix), nil
}

// findMetaMatches scans rendered buffer entries for meta lines of the
// given height (with optional hash-prefix checksum). It returns the entry
// indices of matches; callers print each with its preceding head entry.
func findMetaMatches(lines []string, height uint64, suffix string) []int {
	want := strconv.FormatUint(height, 10)
	var out []int
	for i, line := range lines {
		m := findMetaRe.FindStringSubmatch(stripANSIForFind(line))
		if m == nil || m[1] != want {
			continue
		}
		if suffix != "" && !strings.HasPrefix(strings.ToLower(m[2]), suffix) {
			continue
		}
		out = append(out, i)
	}
	return out
}

// formatQuote renders one quote preview line from a resolved head entry:
// "| ↩ #height: first line…", truncated to maxRunes for narrow screens.
// Pending (placeholder) quotes carry the grey wrapper plus ⏳ so the erase
// region check keeps passing; confirmed quotes render plain.
func formatQuote(height uint64, headEntry string, pending bool, maxRunes int) string {
	line := fmt.Sprintf("|   ┌─  ↩ #%d: %s", height, quoteFirstLine(headEntry, maxRunes))
	if pending {
		return "\x1b[90m" + line + " ⏳\x1b[0m"
	}
	return line
}

// formatQuoteRich renders a quote from the indexed wire itself instead of
// a parsed buffer line: time, author and a chain-content verdict join the
// preview. Falls back to formatQuote when the caller only has text.
func formatQuoteRich(wire WireMessage, pending bool, maxRunes int) string {
	mark := "✓"
	if err := verifyWireContent(wire); err != nil {
		mark = "✗"
	}
	line := fmt.Sprintf("|   ┌─  ↩ #%d | %s %s %s: %s",
		wire.ChainHeight, wire.Time, wire.DisplayName, mark,
		quoteFirstLine(wire.Text, maxRunes))
	if pending {
		return "\x1b[90m" + line + " ⏳\x1b[0m"
	}
	return line
}

// quoteFirstLine strips a rendered head entry (or raw text) down to one
// truncated preview line. Single-line output keeps placeholder erase
// math exact.
func quoteFirstLine(s string, maxRunes int) string {
	first := stripANSIForFind(s)
	if i := strings.Index(first, "\n"); i >= 0 {
		first = first[:i]
	}
	first = strings.TrimSpace(filter.SanitizeForDisplay(strings.TrimPrefix(strings.TrimSpace(first), "|")))
	if maxRunes < 20 {
		maxRunes = 20
	}
	if runes := []rune(first); len(runes) > maxRunes {
		first = string(runes[:maxRunes]) + "…"
	}
	return first
}

// wantsMeta reports whether a wire earns a trailing meta line. Server
// notices (Type system: date, join/leave) stay chained and verified but
// render none: stamping markers with IDs reads silly and nobody
// references them. Legacy lines without chain fields render none either.
func wantsMeta(wire WireMessage, withMeta bool) bool {
	if !withMeta || wire.Type == "system" || wire.ChainHash == "" {
		return false
	}
	return true
}

// wireIndex maps chain heights to full wires for /info lookups.
// Bounded FIFO (~1KB per wire); entries never mutate once stored.
type wireIndex struct {
	cap   int
	order []uint64
	items map[uint64]WireMessage
}

func newWireIndex(capN int) *wireIndex {
	if capN <= 0 {
		capN = 1000
	}
	return &wireIndex{cap: capN, items: make(map[uint64]WireMessage)}
}

// put indexes wires carrying a chain height; legacy lines are skipped.
func (x *wireIndex) put(wire WireMessage) {
	if wire.ChainHeight == 0 {
		return
	}
	if _, ok := x.items[wire.ChainHeight]; !ok {
		x.order = append(x.order, wire.ChainHeight)
	}
	x.items[wire.ChainHeight] = wire
	for len(x.order) > x.cap {
		delete(x.items, x.order[0])
		x.order = x.order[1:]
	}
}

func (x *wireIndex) get(height uint64) (WireMessage, bool) {
	wire, ok := x.items[height]
	return wire, ok
}

// formatInfoBlock renders the full metadata detail of one indexed wire.
// Both verdicts recompute locally without network: the chain content
// hash and, for signed messages, the trip signature.
// formatInfoBlock renders the full metadata detail of one indexed wire.
// Both verdicts recompute locally without network: the chain content
// hash and, for signed messages, the trip signature.
//
// Layout is one field per line with a fixed label width, so every value
// starts at the same column; trip inputs carry an explicit trip prefix
// to never collide with chain-level labels.
func formatInfoBlock(wire WireMessage) []string {
	row := func(label, value string) string {
		return fmt.Sprintf("|   %-13s %s\n", label, value)
	}
	out := []string{fmt.Sprintf("| [Local] #%d — chi tiết metadata:\n", wire.ChainHeight)}
	out = append(out, row("height:", strconv.FormatUint(wire.ChainHeight, 10)))
	out = append(out, row("tmp_id:", strconv.FormatUint(wire.TmpID, 10)))
	out = append(out, row("reply_to:", strconv.FormatUint(wire.ReplyTo, 10)))
	out = append(out, row("hash:", strings.ToLower(wire.ChainHash)))
	out = append(out, row("prev:", strings.ToLower(wire.ChainPrev)))
	out = append(out, row("time:", wire.Time))
	out = append(out, row("from:", wire.DisplayName))
	if wire.Trip != nil {
		tm := wire.Trip
		verdict := "✗"
		detail := ""
		if _, err := trip.Verify(trip.VerifyParams{
			Text:        wire.Text,
			DisplayName: wire.DisplayName,
			ServerPub:   tm.ServerPub,
			PubHex:      tm.Pub,
			Seq:         tm.Seq,
			PrevHex:     tm.Prev,
			SigHex:      tm.Sig,
			MsgHashHex:  tm.MsgHash,
			TmpID:       tm.TmpID,
			ReplyTo:     tm.ReplyTo,
		}); err == nil {
			verdict = "✓"
		} else {
			detail = " (" + err.Error() + ")"
		}
		out = append(out, row("trip:", fmt.Sprintf("◆ %s %s%s", shortBadge(tm.Pub), verdict, detail)))
		out = append(out, row("trip.seq:", strconv.FormatUint(uint64(tm.Seq), 10)))
		// Every signature input, so the verdict above is checkable by
		// eye: sha256(text) against msg_hash, then ed25519 over the
		// payload bytes against sig with pub.
		out = append(out, row("trip.pub:", strings.ToLower(strings.TrimSpace(tm.Pub))))
		out = append(out, row("trip.prev:", strings.ToLower(strings.TrimSpace(tm.Prev))))
		out = append(out, row("trip.sig:", strings.ToLower(strings.TrimSpace(tm.Sig))))
		hashMark := "✗"
		if h := sha256.Sum256([]byte(wire.Text)); hex.EncodeToString(h[:]) == strings.ToLower(strings.TrimSpace(tm.MsgHash)) {
			hashMark = "✓"
		}
		out = append(out, row("trip.hash:", strings.ToLower(strings.TrimSpace(tm.MsgHash))+" "+hashMark))
		out = append(out, row("trip.srv:", strings.ToLower(strings.TrimSpace(tm.ServerPub))))
		out = append(out, row("trip.payload:", fmt.Sprintf("%x", tripPayloadBytes(wire, tm))))
	} else {
		out = append(out, row("trip:", "(không)"))
	}
	if err := verifyWireContent(wire); err == nil {
		out = append(out, row("chain:", "khớp ✓"))
	} else {
		out = append(out, row("chain:", fmt.Sprintf("lệch ✗ (%v)", err)))
	}
	first := stripANSIForFind(wire.Text)
	if i := strings.Index(first, "\n"); i >= 0 {
		first = first[:i]
	}
	first = strings.TrimSpace(filter.SanitizeForDisplay(first))
	if r := []rune(first); len(r) > 80 {
		first = string(r[:80]) + "…"
	}
	out = append(out, row("text:", first))
	out = append(out, row("raw:", rawOneLine(wire.Text)))
	return out
}

// rawOneLine shows message bytes exactly as stored, without any parser
// or renderer: newlines become ⏎ so the block stays one field per line,
// and ESC plus control bytes (except tab) are dropped so legacy history
// containing escape sequences cannot inject terminal output. Documented
// as ESC-neutralized rather than byte-absolute for that reason.
func rawOneLine(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '\n':
			b.WriteRune('⏎')
		case r == '\t':
			b.WriteRune(r)
		case r == '\x1b' || unicode.IsControl(r):
			continue
		default:
			b.WriteRune(r)
		}
	}
	if r := []rune(b.String()); len(r) > 500 {
		return string(r[:500]) + "…"
	}
	return b.String()
}

// tripPayloadBytes rebuilds the exact signed bytes trip.Verify checks
// (same normalization, same field order), so /info shows everything
// needed to re-verify by hand with any ed25519 tool. Nil when the stored
// hex does not decode — the verdict line already reports that case.
func tripPayloadBytes(wire WireMessage, tm *TripMeta) []byte {
	lower := func(s string) string { return strings.ToLower(strings.TrimSpace(s)) }
	pub, err1 := hex.DecodeString(lower(tm.Pub))
	prev, err2 := hex.DecodeString(lower(tm.Prev))
	msgHash, err3 := hex.DecodeString(lower(tm.MsgHash))
	if err1 != nil || err2 != nil || err3 != nil {
		return nil
	}
	return tripcolor.CanonicalPayload(lower(tm.ServerPub), tm.Seq, prev, msgHash, pub, wire.DisplayName, tm.TmpID, tm.ReplyTo)
}

// shortBadge derives the visible badge from a pubkey hex, tolerating junk.
func shortBadge(pubHex string) string {
	h := strings.ToLower(strings.TrimSpace(pubHex))
	if len(h) >= 8 {
		return h[:8]
	}
	return h
}

// quotable reports whether a wire may be quoted or mentioned by ID:
// real chat messages only, never server markers (date/join) or legacy
// lines without a chain height.
func quotable(wire WireMessage) bool {
	return wire.Type == "chat" && wire.ChainHeight > 0
}

// chainTipFile derives the persisted-tip path next to the readline history.
func chainTipFile(historyPath string) string {
	if historyPath == "" {
		historyPath = "history.tmp"
	}
	return filepath.Join(filepath.Dir(historyPath), "chain_tip.json")
}

type chainTipRecord struct {
	Tip       string `json:"tip"`        // hex 64
	Height    uint64 `json:"height"`     // last verified height
	ServerPub string `json:"server_pub"` // lowercase hex; namespaces the tip per server
}

// loadChainTip reads the persisted tip; best effort, false when absent.
// Callers must compare ServerPub with the current server and ignore
// foreign tips (switching servers is a first run, never a fork).
func loadChainTip(path string) ([32]byte, uint64, string, bool) {
	var zero [32]byte
	data, err := os.ReadFile(path)
	if err != nil {
		return zero, 0, "", false
	}
	var rec chainTipRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		return zero, 0, "", false
	}
	tip, ok := chain.ParseHex64(rec.Tip)
	if !ok {
		return zero, 0, "", false
	}
	return tip, rec.Height, rec.ServerPub, true
}

// saveChainTip persists the tip atomically; best effort (ignored on wasm).
func saveChainTip(path string, tip [32]byte, height uint64, serverPub string) {
	data, err := json.Marshal(chainTipRecord{Tip: hex.EncodeToString(tip[:]), Height: height, ServerPub: serverPub})
	if err != nil {
		return
	}
	dir := filepath.Dir(path)
	if dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return
		}
	}
	tmp := filepath.Join(dir, ".tmp-chain-tip.json")
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return
	}
	_ = os.Rename(tmp, path)
}

// verifyWireLink recomputes a received wire link against the running tip.
// It returns the new tip or an error describing the break. Encoding
// dispatches on chain_ver: v2 covers the reply target, v1 (absent) is the
// pre-reply encoding that old records keep verifying against.
func verifyWireLink(wire WireMessage, prev [32]byte) ([32]byte, error) {
	var zero [32]byte
	gotPrev, ok := chain.ParseHex64(wire.ChainPrev)
	if !ok {
		return zero, errChainLink("malformed chain_prev")
	}
	if gotPrev != prev {
		return zero, errChainLink("prev does not continue the local tip")
	}
	want, ok := chain.ParseHex64(wire.ChainHash)
	if !ok {
		return zero, errChainLink("malformed chain_hash")
	}
	if err := verifyWireContent(wire); err != nil {
		return zero, err
	}
	return want, nil
}

// verifyWireContent recomputes a wire link from its own fields, without
// any tip context: it proves the content is intact but says nothing about
// position in the log (that needs the running tip in checkChainLink).
func verifyWireContent(wire WireMessage) error {
	want, ok := chain.ParseHex64(wire.ChainHash)
	if !ok {
		return errChainLink("malformed chain_hash")
	}
	var sig string
	if wire.Trip != nil {
		sig = wire.Trip.Sig
	}
	prev, _ := chain.ParseHex64(wire.ChainPrev)
	var linked bool
	if wire.ChainVer >= 2 {
		linked = chain.VerifyLink(prev, wire.ChainHeight, wire.TmpID, wire.ReplyTo, wire.Type, wire.Time, wire.DisplayName, wire.Text, sig, want)
	} else {
		linked = chain.VerifyLinkV1(prev, wire.ChainHeight, wire.TmpID, wire.Type, wire.Time, wire.DisplayName, wire.Text, sig, want)
	}
	if !linked {
		return errChainLink("hash does not match content")
	}
	return nil
}

type chainLinkError struct{ msg string }

func (e *chainLinkError) Error() string { return "chain link broken: " + e.msg }

func errChainLink(msg string) error { return &chainLinkError{msg} }

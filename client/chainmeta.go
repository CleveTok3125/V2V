package main

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/CleveTok3125/V2V/internal/chain"
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
// otherwise the oldest text match within a minute applies (pre-chain
// servers). It returns -1 when nothing matches.
func matchPendingIndex(pending []pendingMsg, tmpID uint64, text, username, echoDisplay string) int {
	if echoDisplay != username || len(pending) == 0 {
		return -1
	}
	if tmpID != 0 {
		for i, pm := range pending {
			if pm.tmpID == tmpID {
				return i
			}
		}
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

// chainTipFile derives the persisted-tip path next to the readline history.
func chainTipFile(historyPath string) string {
	if historyPath == "" {
		historyPath = "history.tmp"
	}
	return filepath.Join(filepath.Dir(historyPath), "chain_tip.json")
}

type chainTipRecord struct {
	Tip    string `json:"tip"`    // hex 64
	Height uint64 `json:"height"` // last verified height
}

// loadChainTip reads the persisted tip; best effort, false when absent.
func loadChainTip(path string) ([32]byte, uint64, bool) {
	var zero [32]byte
	data, err := os.ReadFile(path)
	if err != nil {
		return zero, 0, false
	}
	var rec chainTipRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		return zero, 0, false
	}
	tip, ok := chain.ParseHex64(rec.Tip)
	if !ok {
		return zero, 0, false
	}
	return tip, rec.Height, true
}

// saveChainTip persists the tip atomically; best effort (ignored on wasm).
func saveChainTip(path string, tip [32]byte, height uint64) {
	data, err := json.Marshal(chainTipRecord{Tip: hex.EncodeToString(tip[:]), Height: height})
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
// It returns the new tip or an error describing the break.
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
	var sig string
	if wire.Trip != nil {
		sig = wire.Trip.Sig
	}
	if !chain.VerifyLink(gotPrev, wire.ChainHeight, wire.TmpID, wire.Type, wire.Time, wire.DisplayName, wire.Text, sig, want) {
		return zero, errChainLink("hash does not match content")
	}
	return want, nil
}

type chainLinkError struct{ msg string }

func (e *chainLinkError) Error() string { return "chain link broken: " + e.msg }

func errChainLink(msg string) error { return &chainLinkError{msg} }

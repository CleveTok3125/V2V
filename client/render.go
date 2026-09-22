package main

// Chat-line rendering and parsing helpers: display rendering
// (SGR/code/linkify), trip-badge verify parsing, URL normalization and
// the server-info probe. Pure top-level functions; the session loop in
// client.go calls them.

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/CleveTok3125/V2V/internal/filter"
	"github.com/CleveTok3125/V2V/internal/markup"
	"github.com/CleveTok3125/V2V/internal/trip"
	"github.com/charmbracelet/x/ansi"
)

func renderChatText(text string) string {
	st := markup.DefaultStyle()
	if ClientCfg != nil {
		cs := ClientCfg.UI.CodeStyle
		st = markup.Style{
			Background: cs.Background,
			Keyword:    cs.Keyword,
			String:     cs.String,
			Comment:    cs.Comment,
			Number:     cs.Number,
			Name:       cs.Name,
			Function:   cs.Function,
			Type:       cs.Type,
			Operator:   cs.Operator,
		}
	}
	return markup.Span(filter.SanitizeForDisplay(text), st)
}

// serverText neutralizes terminal escapes in server-supplied free text
// printed outside the tab buffers (dial notices, info pages, auth errors).
func serverText(s string) string { return filter.SanitizeForDisplay(s) }

// serverField is serverText for one-line fields (host, role, error).
func serverField(s string) string { return filter.SanitizeSingleLine(s) }

func parseTripBadgeLine(line string) (verifyJob, bool) {
	// Look for OSC8 trip link (only https)
	idx := strings.Index(line, "/api/trip/verify?")
	if idx == -1 {
		return verifyJob{}, false
	}
	// Find OSC8 start before idx
	start := strings.LastIndex(line[:idx], "\x1b]8;;")
	if start == -1 {
		return verifyJob{}, false
	}
	// Find URL end (ESC \ terminator)
	endRel := strings.Index(line[idx:], "\x1b\\")
	if endRel == -1 {
		return verifyJob{}, false
	}
	urlStr := line[idx : idx+endRel]
	// Badge is between first terminator and second OSC8
	firstTermEnd := idx + endRel + 2 // after \x1b\\
	secondOsc := strings.Index(line[firstTermEnd:], "\x1b]8;;")
	var badge string
	if secondOsc != -1 {
		badge = strings.TrimSpace(line[firstTermEnd : firstTermEnd+secondOsc])
		// Badge is like "◆ ab12" possibly with color codes: strip all
		// escapes so only the visible text feeds the hash lookup.
		if idx2 := strings.Index(badge, "◆"); idx2 != -1 {
			badge = strings.TrimSpace(ansi.Strip(badge[idx2:]))
		}
	} else {
		// Fallback: find ◆
		if p := strings.Index(line, "◆"); p != -1 {
			end := p + len("◆ ") + 8
			if end > len(line) {
				end = len(line)
			}
			badge = strings.TrimSpace(line[p:end])
		}
	}
	// Parse URL query to get pub/seq etc. Only https is supported now.
	fullURL := urlStr
	if !strings.HasPrefix(fullURL, "http") {
		fullURL = "https://" + fullURL
	}
	u, err := url.Parse(fullURL)
	if err != nil {
		return verifyJob{}, false
	}
	q := u.Query()
	job := verifyJob{
		rawLine:     line,
		badge:       badge,
		urlStr:      urlStr,
		pub:         q.Get("pub"),
		seqStr:      q.Get("seq"),
		prev:        q.Get("prev"),
		sig:         q.Get("sig"),
		msgHash:     q.Get("msg_hash"),
		serverPub:   q.Get("server_pub"),
		displayName: q.Get("display_name"),
		textParam:   q.Get("text"),
	}
	if job.pub == "" || job.sig == "" {
		return verifyJob{}, false
	}
	if v, err := strconv.ParseUint(job.seqStr, 10, 32); err == nil {
		job.seq = uint32(v)
	}
	if v, err := strconv.ParseUint(q.Get("tmp_id"), 10, 64); err == nil {
		job.tmpID = v
	}
	if v, err := strconv.ParseUint(q.Get("reply_to"), 10, 64); err == nil {
		job.tmpReplyTo = v
	}
	return job, true
}

func isTripBadgeLine(line string) bool {
	return strings.Contains(line, "◆") && strings.Contains(line, "/api/trip/verify")
}

func isJoinLeaveSystemLine(line string) bool {
	return strings.Contains(line, "[Hệ thống]:") && (strings.Contains(line, "đã tham gia") || strings.Contains(line, "đã rời"))
}

func isDateBannerLine(line string) bool {
	return strings.Contains(line, "--- Ngày ") && strings.Contains(line, " ---")
}

// isJoinLeave reports whether a system wire is a join/leave notice,
// preferring the machine tag and falling back to text for legacy
// untagged lines.
func isJoinLeave(wire WireMessage) bool {
	if wire.SysKind != "" {
		return wire.SysKind == "join" || wire.SysKind == "leave"
	}
	return isJoinLeaveSystemLine(wire.Text)
}

// isDateBanner reports whether a system wire is a date banner, same
// tag-first fallback scheme as isJoinLeave.
func isDateBanner(wire WireMessage) bool {
	if wire.SysKind != "" {
		return wire.SysKind == "date"
	}
	return isDateBannerLine(wire.Text)
}

func isHistoryBoundaryLine(line string) bool {
	return strings.Contains(line, "--- Lịch sử chat gần đây ---") || strings.Contains(line, "--- Lịch sử cũ ---") || strings.Contains(line, "--- Kết thúc lịch sử")
}

// isOlderSegmentHeader reports the on-demand segment header. Segment
// lines render and index only: they predate the running tip, so link
// verification and echo matching must skip them.
func isOlderSegmentHeader(line string) bool {
	return strings.Contains(line, "--- Lịch sử cũ ---")
}

// parseHistoryBoundary reports whether line opens (header) or closes
// (footer) a history replay. Sync tracking must run regardless of the
// join-display toggle: gating it on showJoin leaves inSync unset, which
// both disables the fork check and feeds replay lines to echo matching.
func parseHistoryBoundary(line string) (boundary bool, start bool) {
	if strings.Contains(line, "--- Lịch sử chat gần đây ---") || strings.Contains(line, "--- Lịch sử cũ ---") {
		return true, true
	}
	// No trailing " ---": counted footers read "(sent/total) ---".
	if strings.Contains(line, "--- Kết thúc lịch sử") {
		return true, false
	}
	return false, false
}

// collectCodeblock gathers a fenced code block after its opening line.
// It returns the joined text, or canceled=true when the user aborted with
// Ctrl+C or the stream ended — in which case the caller must discard
// everything and send nothing.
func collectCodeblock(term inputTerminal, firstLine string) (string, bool) {
	rawLines := []string{firstLine}

	term.SetPrompt("| ... ")
	defer term.SetPrompt("| > ")
	// Cap the collection: an unclosed fence on an endless stream must
	// not grow the buffer without bound.
	const maxCodeblockLines = 512
	for len(rawLines) < maxCodeblockLines {
		nextLine, err := term.ReadLine()
		if err != nil {
			return "", true
		}
		rawLines = append(rawLines, nextLine)

		if strings.HasSuffix(strings.TrimSpace(nextLine), "```") {
			break
		}
	}

	return strings.Join(rawLines, "\n"), false
}

func normalizeURL(input string) string {
	input = strings.TrimSpace(input)
	if !strings.Contains(input, "://") {
		input = "wss://" + input
	}
	u, err := url.Parse(input)
	if err != nil || u.Host == "" {
		return input
	}
	// Rewrite only the scheme: replacing the substring would also hit
	// an inner http:// inside the path, and schemes are
	// case-insensitive. Unknown schemes pass through untouched.
	switch strings.ToLower(u.Scheme) {
	case "http":
		u.Scheme = "ws"
	case "https":
		u.Scheme = "wss"
	case "ws", "wss":
	default:
		return input
	}
	if u.Path == "" || u.Path == "/" {
		u.Path = "/ws"
	}
	return u.String()
}

func checkServerInfo(input string) {
	input = strings.TrimSpace(input)

	if strings.HasPrefix(input, "ws://") {
		input = strings.Replace(input, "ws://", "http://", 1)
	} else if strings.HasPrefix(input, "wss://") {
		input = strings.Replace(input, "wss://", "https://", 1)
	} else if !strings.HasPrefix(input, "http://") && !strings.HasPrefix(input, "https://") {
		if strings.Contains(input, "localhost") || strings.HasPrefix(input, "127.") {
			input = "http://" + input
		} else {
			input = "https://" + input
		}
	}

	client, err := versionHTTPClient()
	if err != nil {
		fmt.Println("❌ Lỗi khi lấy thông tin:", err)
		return
	}
	body, err := fetchServerInfoBody(client, input)
	if err != nil {
		fmt.Println("❌ Lỗi khi lấy thông tin:", err)
		return
	}

	fmt.Println("\n" + serverText(body))
}

// fetchServerInfoBody GETs a server info page over a caller-supplied
// client, so --info rides the session proxy (tor) like the version
// pre-check instead of always going direct.
func fetchServerInfoBody(client *http.Client, url string) (string, error) {
	resp, err := client.Get(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	// The info page is server-supplied: bound the read so a hostile or
	// broken server cannot stream the client out of memory.
	body, err := io.ReadAll(io.LimitReader(resp.Body, 256<<10))
	if err != nil {
		return "", err
	}
	return string(body), nil
}

// Session render methods (moved from main).
func greeting(w io.Writer, uname string) {
	fmt.Fprintln(w, "Đã kết nối với username:", serverField(uname))
	fmt.Fprint(w, "Gõ tin nhắn để chat, /help để hiện trợ giúp\n\n")
}

// refreshCoalesced repaints at most every 100ms under burst; a
// trailing timer guarantees the last update is never swallowed, so
// single messages still appear instantly.
func (s *Session) refreshCoalesced() {
	s.Display.RefreshMu.Lock()
	if time.Since(s.Display.LastRefresh) >= 100*time.Millisecond {
		s.Display.LastRefresh = time.Now()
		s.Display.RefreshMu.Unlock()
		s.Display.Term.Refresh()
		return
	}
	if !s.Display.RefreshPending {
		s.Display.RefreshPending = true
		time.AfterFunc(100*time.Millisecond, func() {
			s.Display.RefreshMu.Lock()
			s.Display.RefreshPending = false
			s.Display.LastRefresh = time.Now()
			s.Display.RefreshMu.Unlock()
			s.Display.Term.Refresh()
		})
	}
	s.Display.RefreshMu.Unlock()
}

// erasePlaceholderLocked splices a placeholder block out of the tab
// buffer and rewrites the screen region, reprinting any lines that
// intervened after it. Caller must hold s.Display.DisplayMu.
// consumeEchoLocked matches a server echo of our own message against
// pending placeholders by exact tmp_id (duplicate texts stay
// unambiguous), with the legacy oldest-text match as fallback. A match
// drops the entry and erases the placeholder. An echo from us that
// matches nothing is stashed: it may have beaten its placeholder
// (local echo race) and is retried when placeholders are tracked;
// entries never matched expire with a warning, which is how
// server-side ID tampering surfaces. allowStash is false for history
// replay: our own old messages must never pollute the stash (their
// tmpIDs belong to previous sessions). Caller must hold s.Display.DisplayMu.

// badgeForWire verifies a trip badge and builds its colored display
// plus the manual-verify hyperlink (kept, opens the stateless API).
// av selects verified vs plain rendering; it is part of the render
// cache key, so toggling /autoverify never serves stale colors.
func (s *Session) badgeForWire(wire WireMessage, av bool) (colored, urlStr string) {
	h := sha256.Sum256([]byte(wire.Trip.Pub))
	plain := "◆ " + hex.EncodeToString(h[:])[:8]
	if av {
		res, err := trip.Verify(trip.VerifyParams{
			Text:        wire.Text,
			DisplayName: wire.DisplayName,
			ServerPub:   wire.Trip.ServerPub,
			PubHex:      wire.Trip.Pub,
			Seq:         wire.Trip.Seq,
			PrevHex:     wire.Trip.Prev,
			SigHex:      wire.Trip.Sig,
			MsgHashHex:  wire.Trip.MsgHash,
			TmpID:       wire.Trip.TmpID,
			ReplyTo:     wire.Trip.ReplyTo,
		})
		if err == nil && res != nil {
			colored = badgeColor(res.Badge) + res.Badge + "\x1b[0m"
		} else {
			colored = "\x1b[91m" + plain + " ✗\x1b[0m"
		}
	} else {
		colored = plain
	}
	if u, err := url.Parse(s.WSURL); err == nil && u.Host != "" {
		// No text= param: msg_hash suffices for verification, keeping
		// links bounded and content out of URLs/history. Old links
		// carrying text keep working (server checks it when present).
		// Every server-supplied field is percent-encoded so a crafted
		// value cannot break out of the OSC8 target.
		q := url.Values{}
		q.Set("pub", wire.Trip.Pub)
		q.Set("seq", strconv.FormatUint(uint64(wire.Trip.Seq), 10))
		q.Set("prev", wire.Trip.Prev)
		q.Set("sig", wire.Trip.Sig)
		q.Set("msg_hash", wire.Trip.MsgHash)
		q.Set("server_pub", wire.Trip.ServerPub)
		q.Set("display_name", wire.DisplayName)
		q.Set("tmp_id", strconv.FormatUint(wire.Trip.TmpID, 10))
		q.Set("reply_to", strconv.FormatUint(wire.Trip.ReplyTo, 10))
		urlStr = "https://" + u.Host + "/api/trip/verify?" + q.Encode()
	}
	return colored, urlStr
}

// renderChatBlock renders one wire message as content rows plus exactly
// one trailing meta line ("  └─  #height:hash | ✍️ badge"). Legacy
// lines without chain fields render content only. System wires render
// sanitized text to their classified tab. Caller must hold s.Display.DisplayMu.

// resolveMentionLocked reports whether @#height names a buffered
// message (suffix checksum when given). Unresolved mentions render
// plain so evicted targets never mislead. Caller must hold s.Display.DisplayMu.
func (s *Session) resolveMentionLocked(height uint64, suffix string) bool {
	return len(findMetaMatches(s.Display.TabChat.lines, height, suffix)) > 0
}

// quoteLinesFor resolves a reply target to quote preview lines from
// either tab buffer (chat first). Nil when evicted. The preview shows
// the target's head line, so liars quoting strangers expose themselves
// to every receiver resolving locally. Caller must hold s.Display.DisplayMu.
func (s *Session) quoteLinesFor(replyTo uint64, pending bool) []string {
	// Rich path first: the struct carries clean time/author plus a
	// content verdict. System wires are never quotable (date/join
	// markers), only chat. Buffer fallback keeps legacy heads quotable.
	if wire, ok := s.Chain.WireIdx.get(replyTo); ok && quotable(wire) {
		return []string{formatQuoteRich(wire, pending, ClientCfg.QuoteMaxRunes())}
	}
	for _, buf := range []*tabBuffer{s.Display.TabChat, s.Display.TabSys} {
		for _, idx := range findMetaMatches(buf.lines, replyTo, "") {
			if idx == 0 {
				continue
			}
			return []string{formatQuote(replyTo, buf.lines[idx-1], pending, ClientCfg.QuoteMaxRunes())}
		}
	}
	return nil
}

// buildChatBlock renders one wire into head + trailing meta strings
// without emitting. Pure given (wire, av, withMeta); cached by renderChatBlock.
func (s *Session) buildChatBlock(wire WireMessage, av, withMeta bool) (quote []string, head, meta string, tab int, hasMeta bool) {
	tab = TabChat
	if wire.Type == "system" {
		head = fmt.Sprintf("| %s\n", filter.SanitizeForDisplay(wire.Text))
		return quote, head, "", classifyTab(wire.Text), wantsMeta(wire, withMeta)
	}
	mentionOpen, mentionClose := mentionSGR(ClientCfg.MentionColor())
	head = fmt.Sprintf("| %s %s: %s\n", filter.SanitizeSingleLine(wire.Time), filter.SanitizeSingleLine(wire.DisplayName), renderMentions(renderChatText(wire.Text), s.resolveMentionLocked, ClientCfg.MentionEnabled(), mentionOpen, mentionClose))
	if wire.ReplyTo > 0 {
		quote = s.quoteLinesFor(wire.ReplyTo, false)
	}
	if !wantsMeta(wire, withMeta) {
		return quote, head, "", tab, false
	}
	meta = metaLineFor(wire.ChainHeight, wire.ChainHash, "")
	if wire.Trip != nil {
		colored, urlStr := s.badgeForWire(wire, av)
		badge := colored
		if urlStr != "" {
			badge = fmt.Sprintf("\x1b]8;;%s\x1b\\%s\x1b]8;;\x1b\\", urlStr, colored)
		}
		meta = metaLineFor(wire.ChainHeight, wire.ChainHash, badge)
	}
	return quote, head, fmt.Sprintf("| %s\n", meta), tab, true
}

func (s *Session) renderChatBlock(wire WireMessage) {
	s.Chain.WireIdx.put(wire)
	s.Verify.AutoVerifyMu.RLock()
	av := s.Verify.AutoVerify
	s.Verify.AutoVerifyMu.RUnlock()
	s.Display.ShowMetaMu.RLock()
	withMeta := s.Display.ShowMeta
	s.Display.ShowMetaMu.RUnlock()
	// Replay and tab switches re-render the same immutable wires;
	// the chain hash covers the content, so it is a safe cache key
	// (verify mode and meta visibility are folded in). Messages with
	// mentions or quotes bypass the cache: highlight and quote targets
	// depend on buffer state (eviction), which the key cannot see.
	if wire.ChainHash != "" && !strings.Contains(wire.Text, "@#") && wire.ReplyTo == 0 {
		key := strings.ToLower(wire.ChainHash) + "\x00" + wire.Type +
			"\x00" + map[bool]string{true: "v", false: "p"}[av] +
			"\x00" + map[bool]string{true: "m", false: "n"}[withMeta]
		if hit, ok := s.Chain.RenderCache.get(key); ok {
			s.emitTab(hit.tab, hit.head)
			if hit.hasMeta {
				s.emitTab(hit.tab, hit.meta)
			}
			return
		}
		_, head, meta, tab, hasMeta := s.buildChatBlock(wire, av, withMeta)
		head = maybeCollapse(head, wire, tab)
		s.Chain.RenderCache.put(key, renderedBlock{tab: tab, head: head, meta: meta, hasMeta: hasMeta})
		s.emitTab(tab, head)
		if hasMeta {
			s.emitTab(tab, meta)
		}
		return
	}
	quote, head, meta, tab, hasMeta := s.buildChatBlock(wire, av, withMeta)
	head = maybeCollapse(head, wire, tab)
	for _, q := range quote {
		s.emitTab(tab, q+"\n")
	}
	s.emitTab(tab, head)
	if hasMeta {
		s.emitTab(tab, meta)
	}
}

// switchTab replays the target buffer under a single lock.
func (s *Session) switchTab(n int) {
	if n != TabChat && n != TabSystem {
		return
	}
	s.Display.DisplayMu.Lock()
	defer s.Display.DisplayMu.Unlock()
	if n == s.Display.ActiveTab {
		return
	}
	s.Display.ActiveTab = n
	s.Display.PrintGen++
	fmt.Fprint(s.Display.Out, "\033[H\033[2J")
	var buf *tabBuffer
	if n == TabChat {
		buf = s.Display.TabChat
	} else {
		buf = s.Display.TabSys
	}
	var sb strings.Builder
	for _, l := range buf.lines {
		sb.WriteString(l)
	}
	fmt.Fprint(s.Display.Out, sb.String())
	s.Display.Term.Refresh()
}

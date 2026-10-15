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

	"github.com/CleveTok3125/V2V/lib/filter"
	"github.com/CleveTok3125/V2V/lib/markup"
	"github.com/CleveTok3125/V2V/lib/trip"
	"github.com/CleveTok3125/V2V/lib/wire"
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
	// Bare http(s) URLs are linkify's job (markup only wraps explicit
	// [text](url) and <autolink> forms). Wrapping them here gives every
	// received message clickable links on terminals without a built-in
	// URL matcher and on the xterm.js web build, matching the local echo
	// path, which already calls markup.Linkify.
	return markup.Linkify(markup.Span(filter.SanitizeForDisplay(text), st))
}

// serverText neutralizes terminal escapes in server-supplied free text
// printed outside the tab buffers (dial notices, info pages, auth errors).
func serverText(s string) string { return filter.SanitizeForDisplay(s) }

// serverField is serverText for one-line fields (host, role, error).
func serverField(s string) string { return filter.SanitizeSingleLine(s) }

// authorField is a display name for a header or a quote row. It neutralizes
// the runes that would otherwise read as header structure: a name minted
// before the allowlist can still carry them, and history is not rewritten.
func authorField(s string) string { return filter.NeutralizeDisplayName(filter.SanitizeSingleLine(s)) }

// tabForWire routes a wire to its tab. Every system line belongs to TabSystem
// so tab 2 stays a complete system log, including the history markers and
// date banners that frame the chat stream.
func tabForWire(msg WireMessage) int {
	if msg.Type != "system" {
		return TabChat
	}
	return TabSystem
}

// parseHistoryBoundary reports whether a replay marker opens (header) or
// closes (footer) a window. The server tags each marker with the window it
// names and, for a footer, how it closed, so this reads the wire instead of
// the wording — which is what keeps the state machine, the fork check and
// echo matching from depending on a string a copy edit could change.
func parseHistoryBoundary(tags []string) (boundary bool, start bool) {
	if !wire.HasTag(tags, wire.TagHistory) {
		return false, false
	}
	return true, !wire.HasAnyTag(tags, wire.TagHistoryEnd, wire.TagHistoryExhausted)
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

	client, err := serverHTTPClient()
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

// refreshCoalesced repaints at most every 100ms under burst; a
// trailing timer guarantees the last update is never swallowed, so
// single messages still appear instantly.
func (s *Session) refreshCoalesced() {
	s.Display.RefreshMu.Lock()
	if time.Since(s.Display.LastRefresh) >= 100*time.Millisecond {
		s.Display.LastRefresh = time.Now()
		s.Display.RefreshMu.Unlock()
		s.refreshTerminal()
		return
	}
	if !s.Display.RefreshPending {
		s.Display.RefreshPending = true
		time.AfterFunc(100*time.Millisecond, func() {
			s.Display.RefreshMu.Lock()
			s.Display.RefreshPending = false
			s.Display.LastRefresh = time.Now()
			s.Display.RefreshMu.Unlock()
			s.refreshTerminal()
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

// verifyLinkBase returns "https://<host>" for the connected server, or
// "" when the host is unknown. Badge links must be absolute: the
// display sanitizer drops an OSC8 target that has no http(s) scheme,
// so a host-relative verify path would render as plain text instead of
// a clickable link (native terminals and the xterm.js link handler both
// require a full URL).
func (s *Session) verifyLinkBase() string {
	if u, err := url.Parse(s.WSURL); err == nil && u.Host != "" {
		return "https://" + u.Host
	}
	return ""
}

// absoluteVerifyURL turns a host-relative verify path (as parseTripBadgeLine
// recovers from a legacy rendered line) into an absolute https URL so the
// display sanitizer keeps the OSC8 target. Absolute URLs pass through.
func (s *Session) absoluteVerifyURL(urlStr string) string {
	if urlStr == "" || strings.HasPrefix(urlStr, "http://") || strings.HasPrefix(urlStr, "https://") {
		return urlStr
	}
	if base := s.verifyLinkBase(); base != "" {
		return base + urlStr
	}
	return urlStr
}

// badgeForWire verifies a trip badge and builds its colored display
// plus the manual-verify hyperlink (kept, opens the stateless API).
// av selects verified vs plain rendering; it is part of the render
// cache key, so toggling /autoverify never serves stale colors.
func (s *Session) badgeForWire(msg WireMessage, av bool) (colored, urlStr string) {
	h := sha256.Sum256([]byte(msg.Trip.Pub))
	plain := "◆ " + hex.EncodeToString(h[:])[:8]
	if av {
		res, err := trip.Verify(trip.VerifyParams{
			Text:        msg.Text,
			DisplayName: msg.DisplayName,
			ServerPub:   msg.Trip.ServerPub,
			PubHex:      msg.Trip.Pub,
			Seq:         msg.Trip.Seq,
			PrevHex:     msg.Trip.Prev,
			SigHex:      msg.Trip.Sig,
			MsgHashHex:  msg.Trip.MsgHash,
			TmpID:       msg.Trip.TmpID,
			ReplyTo:     msg.Trip.ReplyTo,
		})
		if err == nil && res != nil {
			colored = badgeStyle(res.Badge).Render(res.Badge)
		} else {
			colored = warnStyle.Render(plain + " ✗")
		}
	} else {
		colored = plain
	}
	if base := s.verifyLinkBase(); base != "" {
		// No text= param: msg_hash suffices for verification, keeping
		// links bounded and content out of URLs/history. Old links
		// carrying text keep working (server checks it when present).
		// Every server-supplied field is percent-encoded so a crafted
		// value cannot break out of the OSC8 target.
		q := url.Values{}
		q.Set("pub", msg.Trip.Pub)
		q.Set("seq", strconv.FormatUint(uint64(msg.Trip.Seq), 10))
		q.Set("prev", msg.Trip.Prev)
		q.Set("sig", msg.Trip.Sig)
		q.Set("msg_hash", msg.Trip.MsgHash)
		q.Set("server_pub", msg.Trip.ServerPub)
		q.Set("display_name", msg.DisplayName)
		q.Set("tmp_id", strconv.FormatUint(msg.Trip.TmpID, 10))
		q.Set("reply_to", strconv.FormatUint(msg.Trip.ReplyTo, 10))
		// Display-only context (not part of the signed payload): the
		// chain height and send timestamp let the verify page line its
		// fields up with /info.
		if msg.ChainHeight > 0 {
			q.Set("height", strconv.FormatUint(msg.ChainHeight, 10))
		}
		if msg.SentAt != "" {
			q.Set("sent_at", msg.SentAt)
		}
		urlStr = base + "/api/trip/verify?" + q.Encode()
	}
	return colored, urlStr
}

// renderChatBlock renders one wire message as one block: an author header
// ("| HH:MM name • #height:hash • ✍️ badge") and the text as indented rows
// under it. Legacy lines without chain fields lose only the number. System
// wires render sanitized text as a single row in their classified tab.
// Caller must hold s.Display.DisplayMu.

// resolveMentionLocked reports whether @#height names a buffered
// message (suffix checksum when given). Unresolved mentions render
// plain so evicted targets never mislead. Caller must hold s.Display.DisplayMu.
func (s *Session) resolveMentionLocked(height uint64, suffix string) bool {
	return entryIndexByHeight(s.Display.TabChat.lines, height, suffix) >= 0
}

// quoteLinesFor resolves a reply target to quote preview lines from
// either tab buffer (chat first). Nil when evicted. The preview shows
// the target's head line, so liars quoting strangers expose themselves
// to every receiver resolving locally. Caller must hold s.Display.DisplayMu.
func (s *Session) quoteLinesFor(replyTo uint64, pending bool) []string {
	// Rich path first: the struct carries clean time/author plus a
	// content verdict. System wires are never quotable (date/join
	// markers), only chat. Buffer fallback keeps legacy heads quotable.
	if s.Chain.WireIdx != nil {
		if wire, ok := s.Chain.WireIdx.get(replyTo); ok && quotable(wire) {
			return []string{formatQuoteRich(wire, pending, ClientCfg.QuoteMaxRunes())}
		}
	}
	for _, buf := range []*tabBuffer{s.Display.TabChat, s.Display.TabSys} {
		if idx := entryIndexByHeight(buf.lines, replyTo, ""); idx >= 0 {
			// The body is a field, so the preview is read rather than parsed
			// back out of the rendered rows.
			if body := buf.lines[idx].body; body != "" {
				return []string{formatQuote(replyTo, body, pending, ClientCfg.QuoteMaxRunes())}
			}
		}
	}
	return nil
}

// replyPreviewLine is the reply target shown above the input box, "" when no
// reply is pending (a draft or a one-shot target). Caller must hold DisplayMu.
func (s *Session) replyPreviewLine() string {
	height := s.Pending.ReplyDraft
	pending := height > 0
	if height == 0 {
		height = s.Pending.PendingReplyTo
	}
	if height == 0 {
		return ""
	}
	for _, q := range s.quoteLinesFor(height, pending) {
		return stripGutter(q)
	}
	return ""
}

// buildChatBlock renders one wire into a buffer entry without emitting. Pure
// given (wire, av, withMeta); cached by renderChatBlock. A part is never
// patched in place: a change means rendering the whole entry again.
func (s *Session) buildChatBlock(msg WireMessage, av, withMeta bool) (e entry, tab int) {
	tab = TabChat
	if msg.Type == "system" {
		e.header = filter.SanitizeForDisplay(msg.Text)
		// sys_date marks a date banner: it anchors older windows and is
		// deduped when paging repeats a day.
		e.banner = msg.SysDate != ""
		e.day = msg.SysDate
		e.kind = kindSystem
		return e, tabForWire(msg)
	}
	// Author and chain position share one header row; the text hangs below
	// it under a shared indent. One shape for every chat line keeps the
	// author column stable and stops a wrapped body from reading as a row
	// of separate messages.
	header := fmt.Sprintf("%s %s",
		filter.SanitizeSingleLine(msg.Time), authorField(msg.DisplayName))
	if wantsMeta(msg, withMeta) {
		badge := ""
		if msg.Trip != nil {
			colored, urlStr := s.badgeForWire(msg, av)
			badge = colored
			if urlStr != "" {
				badge = linkText(urlStr, colored)
			}
		}
		header += " • " + metaLineFor(msg.ChainHeight, msg.ChainHash, badge)
	}
	body := renderMentions(renderChatText(msg.Text), s.resolveMentionLocked, ClientCfg.MentionEnabled(), mentionStyle(ClientCfg.MentionColor()))
	// A parse that swallowed the text (an unclosed fence, say) leaves an
	// empty body: show the raw message instead of an empty block.
	if strings.TrimSpace(stripANSIForFind(body)) == "" && strings.TrimSpace(msg.Text) != "" {
		body = filter.SanitizeForDisplay(msg.Text)
	}
	// A quote rides in the same entry as the message it answers: a message is
	// one entry whatever it carries, so nothing can land between its parts and
	// every consumer — the erase math, the catch-up merge, the quote lookup —
	// sees a single unit.
	var quote strings.Builder
	if msg.ReplyTo > 0 {
		for _, q := range s.quoteLinesFor(msg.ReplyTo, false) {
			if quote.Len() > 0 {
				quote.WriteString("\n")
			}
			quote.WriteString(q)
		}
	}
	e.quote = quote.String()
	e.header = header
	e.body = body
	if s.isMine(msg) {
		e.kind = kindMine
	}
	if wantsMeta(msg, withMeta) {
		e.height = msg.ChainHeight
		e.hashHex = shortHashHex(msg.ChainHash)
	}
	return e, tab
}

// isMine reports whether a wire is one of our own messages: its trip public
// key matches ours, or, without a trip, it carries our display name.
func (s *Session) isMine(msg WireMessage) bool {
	if msg.Trip != nil && len(s.TripPub) > 0 {
		return strings.EqualFold(msg.Trip.Pub, hex.EncodeToString(s.TripPub))
	}
	return s.Username != "" && msg.DisplayName == s.Username
}

func (s *Session) renderChatBlock(msg WireMessage) {
	s.Chain.WireIdx.put(msg)
	s.Verify.AutoVerifyMu.RLock()
	av := s.Verify.AutoVerify
	s.Verify.AutoVerifyMu.RUnlock()
	s.Display.ShowMetaMu.RLock()
	withMeta := s.Display.ShowMeta
	s.Display.ShowMetaMu.RUnlock()
	// Notify gates live printing of informational system notices; the
	// lines still land in their tab buffer when muted.
	live := s.notifyWireAllowed(msg)
	// A consumed echo owns the slot its pending block was drawn in, so the
	// message lands there instead of beside it. The slot is claimed by the
	// first entry this render emits, which is the message itself: the quote
	// now rides inside it, so one render is one entry.
	emit := func(tab int, e entry) {
		if tab == TabChat {
			s.Pending.Mu.Lock()
			pm := s.Pending.EchoSlot
			s.Pending.EchoSlot = nil
			s.Pending.Mu.Unlock()
			if pm != nil {
				if s.replaceSlot(pm.slot, e) {
					return
				}
			}
		}
		s.emitTabLive(live, tab, e)
	}
	// Replay and tab switches re-render the same immutable wires;
	// the chain hash covers the content, so it is a safe cache key
	// (verify mode and meta visibility are folded in). Messages with
	// mentions or quotes bypass the cache: highlight and quote targets
	// depend on buffer state (eviction), which the key cannot see.
	if msg.ChainHash != "" && !strings.Contains(msg.Text, "@#") && msg.ReplyTo == 0 {
		key := strings.ToLower(msg.ChainHash) + "\x00" + msg.Type +
			"\x00" + map[bool]string{true: "v", false: "p"}[av] +
			"\x00" + map[bool]string{true: "m", false: "n"}[withMeta]
		if hit, ok := s.Chain.RenderCache.get(key); ok {
			emit(hit.tab, hit.entry)
			return
		}
		e, tab := s.buildChatBlock(msg, av, withMeta)
		e = maybeCollapse(e, msg, tab)
		s.Chain.RenderCache.put(key, renderedBlock{tab: tab, entry: e})
		emit(tab, e)
		return
	}
	e, tab := s.buildChatBlock(msg, av, withMeta)
	e = maybeCollapse(e, msg, tab)
	emit(tab, e)
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
	// A tab switch repaints from the buffers; a held catch-up must
	// print first or the switch would hide it.
	s.releaseCatchupLocked()
	s.Display.ActiveTab = n
	// The repaint leaves the cursor at the end of the whole buffer, so
	// nothing drawn earlier can still be erased by moving up.
	s.Display.LayoutEpoch++
	if s.Display.Sink != nil {
		// The app renders whichever tab is active; it just needs to know.
		s.Display.Sink.Notify()
	}
}

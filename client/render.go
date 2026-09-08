package main

// Chat-line rendering and parsing helpers: display rendering
// (SGR/code/linkify), trip-badge verify parsing, URL normalization and
// the server-info probe. Pure top-level functions; the session loop in
// client.go calls them.

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/CleveTok3125/V2V/codebg"
	"github.com/CleveTok3125/V2V/internal/filter"
	"github.com/CleveTok3125/V2V/markup"
)

func renderChatText(text string) string {
	st := codebg.DefaultStyle()
	if ClientCfg != nil {
		cs := ClientCfg.UI.CodeStyle
		st = codebg.Style{
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
		// Strip ANSI color if present (should be plain, but handle)
		// Badge is like "◆ ab12" possibly with color codes - strip them for hash
		// For now, badge as visible text without ANSI
		if idx2 := strings.Index(badge, "◆"); idx2 != -1 {
			badge = badge[idx2:]
			// Remove any ANSI inside badge (e.g., color prefix)
			if strings.Contains(badge, "\x1b[") {
				// Strip SGR codes for badge extraction
				badge = strings.TrimSpace(filter.SanitizeForDisplay(badge))
			}
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
	return strings.Contains(line, "--- Lịch sử chat gần đây ---") || strings.Contains(line, "--- Kết thúc lịch sử")
}

// parseHistoryBoundary reports whether line opens (header) or closes
// (footer) a history replay. Sync tracking must run regardless of the
// join-display toggle: gating it on showJoin leaves inSync unset, which
// both disables the fork check and feeds replay lines to echo matching.
func parseHistoryBoundary(line string) (boundary bool, start bool) {
	if strings.Contains(line, "--- Lịch sử chat gần đây ---") {
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

	if !strings.HasPrefix(input, "http://") && !strings.HasPrefix(input, "https://") &&
		!strings.HasPrefix(input, "ws://") && !strings.HasPrefix(input, "wss://") {
		input = "wss://" + input
	}

	input = strings.Replace(input, "http://", "ws://", 1)
	input = strings.Replace(input, "https://", "wss://", 1)

	u, err := url.Parse(input)
	if err == nil {
		if u.Path == "" || u.Path == "/" {
			u.Path = "/ws"
		}
		return u.String()
	}

	return input
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

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(input)
	if err != nil {
		fmt.Println("❌ Lỗi khi lấy thông tin:", err)
		return
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		fmt.Println("❌ Lỗi khi đọc dữ liệu:", err)
		return
	}

	fmt.Println("\n" + string(body))
}

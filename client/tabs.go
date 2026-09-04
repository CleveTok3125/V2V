package main

import (
	"strings"
)

// Tab IDs for the single-terminal switching view: TabChat shows chat and
// trip badges, TabSystem shows local, system, date and history lines.
const (
	TabChat   = 1
	TabSystem = 2
)

// isGenericSystemLine matches unicast system lines that are not join/leave,
// date banners or history boundaries (e.g. rate-limit warnings).
func isGenericSystemLine(line string) bool {
	t := strings.TrimSpace(line)
	return strings.Contains(t, "[Hệ thống]:")
}

// isLocalLine matches lines rendered locally without server round-trip.
func isLocalLine(line string) bool {
	return strings.Contains(line, "[Local]:")
}

// classifyTab routes a rendered line to its tab. Chat (WireMessage and trip
// badge lines) goes to TabChat; system banners, join/leave, history
// boundaries and local lines go to TabSystem.
func classifyTab(line string) int {
	if isTripBadgeLine(line) {
		return TabChat
	}
	// History boundaries delimit the chat history stream, so they belong
	// to TabChat. Date banners, join/leave, generic system and local lines
	// go to TabSystem.
	if isHistoryBoundaryLine(line) {
		return TabChat
	}
	if isJoinLeaveSystemLine(line) || isDateBannerLine(line) || isGenericSystemLine(line) || isLocalLine(line) {
		return TabSystem
	}
	return TabChat
}

// tabBuffer is a FIFO ring holding raw lines for one tab with dual-limit
// eviction (lines + bytes), mirroring server ChatHistory. Caps come from
// ClientCfg, never hardcoded. Callers must hold displayMu.
type tabBuffer struct {
	lines    []string
	size     int
	maxLines int
	maxBytes int
}

func newTabBuffer(maxLines, maxBytes int) *tabBuffer {
	if maxLines <= 0 {
		maxLines = 10000
	}
	if maxBytes <= 0 {
		maxBytes = 2097152
	}
	return &tabBuffer{maxLines: maxLines, maxBytes: maxBytes}
}

func (b *tabBuffer) append(line string) {
	b.lines = append(b.lines, line)
	b.size += len(line)
	for (len(b.lines) > b.maxLines || b.size > b.maxBytes) && len(b.lines) > 0 {
		b.size -= len(b.lines[0])
		b.lines[0] = ""
		b.lines = b.lines[1:]
	}
	if cap(b.lines) > 4*len(b.lines) && cap(b.lines) > 1024 {
		n := make([]string, len(b.lines), len(b.lines))
		copy(n, b.lines)
		b.lines = n
	}
}

// tabCaps resolves buffer caps from config: chat lines derive from the
// existing ui.web.scrollback field so the hidden buffer exactly matches the
// visible xterm capacity; byte caps come from the tabs section.
func tabCaps() (chatLines, chatBytes, sysLines, sysBytes int) {
	chatLines, chatBytes, sysLines, sysBytes = 10000, 2097152, 2000, 409600
	if ClientCfg == nil {
		return chatLines, chatBytes, sysLines, sysBytes
	}
	if ClientCfg.UI.Web.Scrollback > 0 {
		chatLines = ClientCfg.UI.Web.Scrollback
	}
	if ClientCfg.Tabs.ChatMaxBytes > 0 {
		chatBytes = ClientCfg.Tabs.ChatMaxBytes
	}
	if ClientCfg.Tabs.SystemMaxLines > 0 {
		sysLines = ClientCfg.Tabs.SystemMaxLines
	}
	if ClientCfg.Tabs.SystemMaxBytes > 0 {
		sysBytes = ClientCfg.Tabs.SystemMaxBytes
	}
	return chatLines, chatBytes, sysLines, sysBytes
}

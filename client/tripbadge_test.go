package main

import (
	"fmt"
	"net/url"
	"strings"
	"testing"
)

// badgeTestURL builds a verify URL in the exact query shape
// badgeForWire emits (render.go), so parse tests pin the real
// round trip, not a hand-waved fixture.
func badgeTestURL(extra string) string {
	v := url.Values{}
	v.Set("pub", "aa11")
	v.Set("seq", "7")
	v.Set("prev", "bb22")
	v.Set("sig", "cc33")
	v.Set("msg_hash", "dd44")
	v.Set("server_pub", "ee55")
	v.Set("display_name", "Alice#1234")
	v.Set("tmp_id", "9")
	v.Set("reply_to", "3")
	return "https://chat.example.com/api/trip/verify?" + v.Encode() + extra
}

// badgeTestLine wraps url + badge in the exact OSC8 construct
// buildChatBlock emits, prefixed with a realistic meta head.
func badgeTestLine(u, badge string) string {
	return fmt.Sprintf("|   \u2514\u2500  #7:abcd | \x1b]8;;%s\x1b\\%s\x1b]8;;\x1b\\", u, badge)
}

func checkBadgeJob(t *testing.T, job verifyJob, line, u, badge string) {
	t.Helper()
	if job.rawLine != line {
		t.Error("rawLine not preserved")
	}
	if job.urlStr != u[strings.Index(u, "/api/trip"):] {
		t.Errorf("urlStr = %q", job.urlStr)
	}
	if job.badge != badge {
		t.Errorf("badge = %q, want %q", job.badge, badge)
	}
	if job.pub != "aa11" || job.sig != "cc33" || job.prev != "bb22" ||
		job.msgHash != "dd44" || job.serverPub != "ee55" {
		t.Errorf("crypto fields mangled: %+v", job)
	}
	if job.seq != 7 || job.seqStr != "7" {
		t.Errorf("seq = %d/%q", job.seq, job.seqStr)
	}
	if job.displayName != "Alice#1234" {
		t.Errorf("displayName = %q", job.displayName)
	}
	if job.tmpID != 9 || job.tmpReplyTo != 3 {
		t.Errorf("tmpID = %d replyTo = %d", job.tmpID, job.tmpReplyTo)
	}
}

func TestParseTripBadgeLineRoundTrip(t *testing.T) {
	u := badgeTestURL("")
	line := badgeTestLine(u, "◆ ab12cd34")
	job, ok := parseTripBadgeLine(line)
	if !ok {
		t.Fatal("plain badge line must parse")
	}
	checkBadgeJob(t, job, line, u, "◆ ab12cd34")
	if job.textParam != "" {
		t.Errorf("textParam = %q, want empty", job.textParam)
	}
}

func TestParseTripBadgeLineColored(t *testing.T) {
	u := badgeTestURL("")
	line := badgeTestLine(u, "\x1b[91m◆ ab12cd34 ✗\x1b[0m")
	job, ok := parseTripBadgeLine(line)
	if !ok {
		t.Fatal("colored badge line must parse")
	}
	if !strings.Contains(job.badge, "◆") {
		t.Errorf("badge lost the marker: %q", job.badge)
	}
	if strings.Contains(job.badge, "\x1b") {
		t.Errorf("badge kept escape bytes: %q", job.badge)
	}
	if job.pub != "aa11" || job.sig != "cc33" {
		t.Errorf("crypto fields mangled: %+v", job)
	}
}

func TestParseTripBadgeLineLegacyText(t *testing.T) {
	u := badgeTestURL("&text=" + url.QueryEscape("hello world"))
	line := badgeTestLine(u, "◆ ab12cd34")
	job, ok := parseTripBadgeLine(line)
	if !ok {
		t.Fatal("legacy text= link must parse")
	}
	if job.textParam != "hello world" {
		t.Errorf("textParam = %q", job.textParam)
	}
}

func TestParseTripBadgeLineNoCloseFallback(t *testing.T) {
	u := badgeTestURL("")
	line := "| x \x1b]8;;" + u + "\x1b\\" + "◆ ab12cd34"
	job, ok := parseTripBadgeLine(line)
	if !ok {
		t.Fatal("line without closing OSC8 must use the ◆ fallback")
	}
	if job.badge != "◆ ab12cd34" {
		t.Errorf("fallback badge = %q", job.badge)
	}
}

func TestParseTripBadgeLineRejects(t *testing.T) {
	if _, ok := parseTripBadgeLine("| plain line"); ok {
		t.Error("line without verify marker must not parse")
	}
	u := badgeTestURL("")
	noSig := strings.Replace(u, "sig=cc33&", "", 1)
	if _, ok := parseTripBadgeLine(badgeTestLine(noSig, "◆ ab12cd34")); ok {
		t.Error("link without sig must not parse")
	}
}

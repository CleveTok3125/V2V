package main

import (
	"bytes"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"unicode/utf8"
)

// Every log line starts with a visible marker, so a scan down the file finds
// the warn lines by shape and a line never reads as a bare sentence. The geoip
// package is the one loader whose warnings carry no emoji of their own, so its
// call site adds one; without that, the boot prints two warn lines that look
// unlike every other warn line beside them.
func TestGeoipWarningsCarryTheLeadingEmoji(t *testing.T) {
	var buf bytes.Buffer
	old := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(old) })

	logGeoipWarnings([]string{
		"geoip: ./config/geoip missing; ASN/region grouping disabled",
		"geoip: no database in ./config/geoip; ASN/region grouping disabled",
	})

	seen := 0
	for _, line := range strings.Split(buf.String(), "\n") {
		if !strings.Contains(line, "geoip:") {
			continue
		}
		seen++
		idx := strings.Index(line, "[WARN] ")
		if idx < 0 {
			t.Fatalf("line is not a warn line: %q", line)
		}
		marker, _ := utf8.DecodeRuneInString(line[idx+len("[WARN] "):])
		if marker < 0x2000 {
			t.Fatalf("warn line has no leading emoji: %q", line)
		}
	}
	if seen != 2 {
		t.Fatalf("want one line per warning, saw %d in %q", seen, buf.String())
	}
}

// logSeverityEmoji is the level each severity emoji belongs to. An outcome
// marker must not sit on a level that says the opposite: a warn marker on an
// INFO line, or a success marker on a WARN line, contradicts itself and makes a
// scan down the log lie. The emoji that mark the kind of event rather than its
// severity (🟢 join, 🔴 leave, 🛑 shutdown, 🔄 reload, and the rest) are not
// listed, because their level is whatever the event is worth.
var logSeverityEmoji = map[string]string{
	"\u26a0\ufe0f": "WARN", // ⚠️
	"\u26d4":       "WARN", // ⛔
	"\u274c":       "ERROR",
	"\U0001f6a8":   "ERROR",
	"\u2705":       "INFO",
	"\U0001f7e2":   "INFO",
}

// TestLogEmojiMatchesLevel holds the rule over the call sites: a line that
// opens with a severity emoji has to be logged at the level that emoji means.
func TestLogEmojiMatchesLevel(t *testing.T) {
	levels := map[string]string{
		"logInfo": "INFO", "logInfof": "INFO",
		"logWarn": "WARN", "logWarnf": "WARN",
		"logError": "ERROR", "logErrorf": "ERROR",
	}
	call := regexp.MustCompile(`(logInfo|logInfof|logWarn|logWarnf|logError|logErrorf)\(\s*"([^"]*)"`)

	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		src, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range call.FindAllStringSubmatch(string(src), -1) {
			function, format := m[1], m[2]
			for emoji, want := range logSeverityEmoji {
				if !strings.HasPrefix(format, emoji) {
					continue
				}
				checked++
				if got := levels[function]; got != want {
					t.Errorf("%s: %s opens with %q, which means %s, not %s",
						file, function, emoji, want, got)
				}
			}
		}
	}
	if checked < 50 {
		t.Fatalf("only %d severity lines matched; the scan is not seeing the call sites", checked)
	}
}

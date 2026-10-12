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

// logCall is one log call site with the literal format it opens with. The tests
// read the source rather than the binary because the rule is about the line a
// call site writes, not about the value one particular call produced.
type logCall struct {
	file     string
	function string
	format   string
}

var (
	logCallSite = regexp.MustCompile(`(logInfo|logInfof|logWarn|logWarnf|logError|logErrorf)\(\s*"([^"]*)"`)
	logTagGroup = regexp.MustCompile(`^\S+ \(([a-z0-9_]+(?:,[a-z0-9_]+)*)\) `)
)

// logCalls walks the package sources and returns every log call site.
func logCalls(t *testing.T) []logCall {
	t.Helper()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	var out []logCall
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") || file == "loglevel.go" {
			continue
		}
		src, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range logCallSite.FindAllStringSubmatch(string(src), -1) {
			out = append(out, logCall{file: file, function: m[1], format: m[2]})
		}
	}
	if len(out) < 100 {
		t.Fatalf("only %d log call sites found; the scan is not seeing the sources", len(out))
	}
	return out
}

// TestLogCallSitesUseDeclaredTags holds the standard over the call sites: every
// line opens with "<emoji> (<tag>[,<tag>]) ", every tag is declared, and every
// declared tag is used. The catalog is the vocabulary, so a typo cannot invent
// a tag that exists only in production, and a tag that no line uses is a
// promise nothing keeps.
func TestLogCallSitesUseDeclaredTags(t *testing.T) {
	used := map[string]int{}
	for _, call := range logCalls(t) {
		m := logTagGroup.FindStringSubmatch(call.format)
		if m == nil {
			t.Errorf(`%s: %s format %q does not open with "<emoji> (<tag>) "`,
				call.file, call.function, call.format)
			continue
		}
		seen := map[string]bool{}
		for _, tag := range strings.Split(m[1], ",") {
			if seen[tag] {
				t.Errorf("%s: %s repeats the tag %q", call.file, call.function, tag)
			}
			seen[tag] = true
			if !hasLogTag(tag) {
				t.Errorf("%s: %s uses the tag %q, which the catalog does not declare",
					call.file, call.function, tag)
				continue
			}
			used[tag]++
		}
	}
	for _, tag := range logTagCatalog {
		if used[tag.tag] == 0 {
			t.Errorf("the tag %q is declared but no call site uses it", tag.tag)
		}
	}
}

// TestLogTagsAreDocumented keeps the catalog and the spec in step in both
// directions: a tag the code can write has to be explained somewhere, and a
// table row for a tag the code cannot write is a promise nothing keeps.
func TestLogTagsAreDocumented(t *testing.T) {
	doc, err := os.ReadFile(filepath.Join("..", "docs", "LOG_STANDARD.md"))
	if err != nil {
		t.Fatalf("docs/LOG_STANDARD.md unavailable: %v", err)
	}
	for _, tag := range logTagCatalog {
		if !bytes.Contains(doc, []byte("`"+tag.tag+"`")) {
			t.Errorf("the tag %q is in the catalog but not documented in docs/LOG_STANDARD.md", tag.tag)
		}
	}
	row := regexp.MustCompile("^\\| `([a-z0-9_]+)` \\|")
	for _, line := range strings.Split(string(doc), "\n") {
		m := row.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		if !hasLogTag(m[1]) {
			t.Errorf("docs/LOG_STANDARD.md has a row for %q, which the catalog does not declare", m[1])
		}
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
	checked := 0
	for _, call := range logCalls(t) {
		for emoji, want := range logSeverityEmoji {
			if !strings.HasPrefix(call.format, emoji) {
				continue
			}
			checked++
			if got := levels[call.function]; got != want {
				t.Errorf("%s: %s opens with %q, which means %s, not %s",
					call.file, call.function, emoji, want, got)
			}
		}
	}
	if checked < 50 {
		t.Fatalf("only %d severity lines matched; the scan is not seeing the call sites", checked)
	}
}

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

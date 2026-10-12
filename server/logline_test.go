package main

import (
	"bytes"
	"log"
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

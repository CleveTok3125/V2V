package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func stripMust(t *testing.T, in string) string {
	t.Helper()
	out, err := StripComments([]byte(in))
	if err != nil {
		t.Fatalf("strip %q: %v", in, err)
	}
	return string(out)
}

func TestStripLineComment(t *testing.T) {
	got := stripMust(t, "{\"a\": 1 /* one */, \"b\": 2} // trailing")
	if strings.Contains(got, "//") || strings.Contains(got, "/*") {
		t.Fatalf("comment survived: %q", got)
	}
	var v map[string]int
	if err := json.Unmarshal([]byte(got), &v); err != nil {
		t.Fatalf("stripped output must parse: %v (%q)", err, got)
	}
	if v["a"] != 1 || v["b"] != 2 {
		t.Fatalf("values altered: %q", got)
	}
}

func TestStripCommentMarkersInsideStringsSurvive(t *testing.T) {
	in := `{"url": "http://x/y", "glob": "/* not a comment", "q": "say \"//hi\""}`
	got := stripMust(t, in)
	if got != in {
		t.Fatalf("string contents altered:\n got %q\nwant %q", got, in)
	}
}

func TestStripBlockComment(t *testing.T) {
	got := stripMust(t, "{\"a\": /* multi\nline */ 1}")
	var v map[string]int
	if err := json.Unmarshal([]byte(got), &v); err != nil || v["a"] != 1 {
		t.Fatalf("block strip failed: %v (%q)", err, got)
	}
}

func TestStripLineCommentAtEOFAccepts(t *testing.T) {
	got := stripMust(t, "{\"a\": 1}// no newline")
	var v map[string]int
	if err := json.Unmarshal([]byte(got), &v); err != nil {
		t.Fatalf("EOF comment must be fine: %v", err)
	}
}

func TestStripCRLFComment(t *testing.T) {
	got := stripMust(t, "{\"a\": 1} // c\r\n{\"b\": 2}")
	if strings.Contains(got, "//") {
		t.Fatalf("CRLF comment survived: %q", got)
	}
}

func TestStripUnterminatedBlockErrors(t *testing.T) {
	if _, err := StripComments([]byte("{\"a\": /* oops")); err == nil {
		t.Fatal("unterminated block comment must error, not swallow the file")
	}
}

func TestStripCommentDelimitersAloneSurvive(t *testing.T) {
	// A lone slash is not valid JSON anyway; the stripper must pass it
	// through untouched so encoding/json reports the real error.
	got := stripMust(t, "{\"a\": 1} /")
	if !strings.Contains(got, "/") {
		t.Fatalf("lone slash altered: %q", got)
	}
}

func TestTemplateParsesAsJSONC(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "template", "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	stripped, err := StripComments(raw)
	if err != nil {
		t.Fatal(err)
	}
	// Decode into any: this pins strip validity only. Template-to-struct
	// drift (e.g. charAspect float vs int) is separate debt, out of scope.
	var v map[string]any
	if err := json.Unmarshal(stripped, &v); err != nil {
		t.Fatalf("template must parse after strip: %v", err)
	}
	if _, ok := v["limits"]; !ok {
		t.Fatal("template must keep the limits section")
	}
	// The template must also decode into the live struct: template and
	// struct drifting apart (e.g. charAspect float vs int) broke the
	// sealed-config flow end to end, so pin them together here.
	var c ClientConfig
	if err := json.Unmarshal(stripped, &c); err != nil {
		t.Fatalf("template must decode into ClientConfig: %v", err)
	}
	if c.Limits.MaxMessageLength != 5000 {
		t.Fatalf("template limits mismatch: %+v", c.Limits)
	}
}

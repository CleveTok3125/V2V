package linkify

import (
	"strings"
	"testing"
)

const (
	url       = "https://example.com/photo.jpg"
	wrapped   = "\x1b]8;;" + url + "\x1b\\\x1b[94;4m" + url + "\x1b[0m\x1b]8;;\x1b\\"
	plainLink = "example.com/photo.jpg"
)

func TestBasicWrap(t *testing.T) {
	got := Linkify("xem " + url + " nha")
	if got != "xem "+wrapped+" nha" {
		t.Errorf("unexpected wrap:\nwant %q\ngot  %q", "xem "+wrapped+" nha", got)
	}
}

func TestMultipleURLs(t *testing.T) {
	got := Linkify(url + " va https://a.b/c")
	if n := strings.Count(got, osc8Open+osc8Close); n != 2 {
		t.Errorf("expected 2 hyperlinks, got %d:\n%q", n, got)
	}
}

func TestTrailingPunctKeptOutside(t *testing.T) {
	got := Linkify("xem " + url + ".")
	if !strings.Contains(got, wrapped+".") {
		t.Errorf("dot must stay outside the link:\n%q", got)
	}
	if strings.Contains(got, "/.\x1b\\") {
		t.Errorf("dot swallowed into link:\n%q", got)
	}
}

func TestAdjacentMultibyte(t *testing.T) {
	got := Linkify("ảnh:https://i.test/ả.jpg（xem）")
	if !strings.Contains(got, "]8;;https://i.test/") {
		t.Errorf("URL next to CJK punctuation not matched:\n%q", got)
	}
}

func TestIdempotent(t *testing.T) {
	in := "đã wrap " + wrapped
	if got := Linkify(in); got != in {
		t.Errorf("double-wrap detected:\nwant %q\ngot  %q", in, got)
	}
}

func TestPassthrough(t *testing.T) {
	for _, in := range []string{"", "không có link", "15:04 alice: chào", "ftp://x/y"} {
		if got := Linkify(in); got != in {
			t.Errorf("passthrough changed %q -> %q", in, got)
		}
	}
}

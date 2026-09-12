package blog

import (
	"strings"
	"testing"
)

const kitchenSink = "# Tieu de\n\nVan ban **dam** va `code inline`.\n\n" +
	"- [ ] chua xong\n- [x] da xong\n\n" +
	"| A | B |\n|---|:--:|\n| 1 | 2 |\n\n" +
	"> quote dong\n\n```go\npackage main\n```\n\n" +
	"```\nplain block\n```\n\n<script>alert('xss')</script>\n"

func TestRenderMarkdownKitchenSink(t *testing.T) {
	out := RenderMarkdown(kitchenSink)
	for _, want := range []string{
		"<h1>Tieu de</h1>",
		"<strong>dam</strong>",
		"<code>code inline</code>",
		"<table>", "<blockquote>",
		`class="chroma"`, // highlighted fenced block
		"<pre><code>plain block", // unknown language stays plain
		"<!-- raw HTML omitted -->", // script neutralized
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "<script>") {
		t.Error("raw <script> leaked through")
	}
}

func TestValidTag(t *testing.T) {
	for _, ok := range FixedTags {
		if !ValidTag(ok) {
			t.Errorf("fixed tag %q rejected", ok)
		}
	}
	for _, bad := range []string{"", "news", "Announcement", "guide "} {
		if ValidTag(bad) {
			t.Errorf("non-fixed tag %q accepted", bad)
		}
	}
}

func TestPostPageEscapesAndChrome(t *testing.T) {
	out := PostPage(`<b>T</b>`, "12/09/2026", []string{"guide", "<x>"}, "<p>body</p>", "/web/blog/cactus.css")
	for _, want := range []string{
		"&lt;b&gt;T&lt;/b&gt;", // title escaped
		`href="/blog/tag/guide"`, "&lt;x&gt;", // tags escaped
		"<p>body</p>",            // library HTML passes through
		`&copy; V2V 2026`,        // exact footer
		`id="theme"`, `id="to-top"`, // chrome present
		`<link rel="stylesheet" href="/web/blog/cactus.css">`,
		`<article class="prose">`, `<div class="masthead">`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q", want)
		}
	}
	for _, banned := range []string{"before:content", "pico", "--fg:"} {
		if strings.Contains(out, banned) {
			t.Errorf("leaked old styling %q", banned)
		}
	}
}

func TestIndexPage(t *testing.T) {
	out := IndexPage([]Item{
		{Slug: "a-b", Title: "Bai A", Date: "01/01/2026"},
		{Slug: "xss\"><", Title: "T", Date: "D"},
	}, "/web/blog/cactus.css")
	if !strings.Contains(out, `href="/blog/a-b"`) {
		t.Error("missing item link")
	}
	if strings.Contains(out, `xss"><`) {
		t.Error("slug not escaped")
	}
}

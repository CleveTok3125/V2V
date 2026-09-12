// Package blog renders the server announcement pages. Templates mirror
// the trimmed astro-theme-cactus layout (see webterm/blog/THIRD_PARTY.md).
// Only four values are interpolated into HTML (title, date, slug, tag)
// and all of them pass through html.EscapeString; post bodies come from
// the markdown pipeline, which escapes raw HTML by default.
package blog

import (
	"bytes"
	"html"
	"strings"

	chromahtml "github.com/alecthomas/chroma/v2/formatters/html"
	"github.com/yuin/goldmark"
	highlighting "github.com/yuin/goldmark-highlighting/v2"
	"github.com/yuin/goldmark/extension"
)

// FixedTags is the closed announcement category set. The management API
// (later phase) rejects any tag outside this list.
var FixedTags = []string{"announcement", "guide", "maintenance"}

// ValidTag reports whether t belongs to the fixed set.
func ValidTag(t string) bool {
	for _, f := range FixedTags {
		if t == f {
			return true
		}
	}
	return false
}

// RenderMarkdown converts a post body to HTML. GFM covers tables, task
// lists and strikethrough; fenced code is highlighted with CSS classes
// styled by cactus.css (no inline colors, follows light/dark).
func RenderMarkdown(md string) string {
	var buf bytes.Buffer
	parser := goldmark.New(
		goldmark.WithExtensions(
			extension.GFM,
			highlighting.NewHighlighting(
				highlighting.WithStyle("github"),
				highlighting.WithFormatOptions(chromahtml.WithClasses(true)),
			),
		),
	)
	if err := parser.Convert([]byte(md), &buf); err != nil {
		return "<p>" + html.EscapeString(md) + "</p>"
	}
	return buf.String()
}

// themeScript follows the system theme, toggles data-theme on click and
// remembers the choice. The back-to-top button appears past 480px.
const themeScript = `<script>(function(){var b=document.getElementById('theme');function sys(){return matchMedia('(prefers-color-scheme: dark)').matches?'dark':'light'}function paint(t){if(t)document.documentElement.setAttribute('data-theme',t);else document.documentElement.removeAttribute('data-theme');b.textContent=(t||sys())==='dark'?'☀︎':'☾'}var t=localStorage.getItem('v2v-theme');paint(t);b.onclick=function(){var cur=document.documentElement.getAttribute('data-theme')||sys();var next=cur==='dark'?'light':'dark';localStorage.setItem('v2v-theme',next);paint(next)};var tb=document.getElementById('to-top');function onScroll(){tb.setAttribute('data-show',(window.scrollY>480).toString())}addEventListener('scroll',onScroll,{passive:true});onScroll();tb.onclick=function(){scrollTo({behavior:'smooth',top:0})}})();</script>`

// layout wraps body in the site chrome. cssPath is the stylesheet URL
// (served from webterm/blog by the static handler).
func layout(title, body, cssPath string) string {
	return "<!DOCTYPE html>\n<html lang=\"vi\">\n<head>\n<meta charset=\"utf-8\">\n" +
		"<meta name=\"viewport\" content=\"width=device-width,initial-scale=1\">\n" +
		"<title>" + html.EscapeString(title) + " — V2V Blog</title>\n" +
		"<link rel=\"stylesheet\" href=\"" + html.EscapeString(cssPath) + "\">\n</head>\n<body>\n" +
		"<header><div class=\"top\"><a class=\"brand\" href=\"/blog/\">V2V Blog</a>" +
		"<button id=\"theme\" title=\"Sáng/Tối\">☾</button></div>" +
		"<nav><ul><li><a href=\"/blog/\">Bài viết</a></li></ul></nav></header>\n" +
		"<main>\n" + body + "\n</main>\n" +
		"<footer><div>&copy; V2V 2026</div>" +
		"<nav><ul><li><a href=\"/blog/\">Bài viết</a></li></ul></nav></footer>\n" +
		"<button id=\"to-top\" data-show=\"false\" title=\"Về đầu trang\">↑</button>\n" +
		themeScript + "\n</body>\n</html>\n"
}

// Item is one list entry on the index page.
type Item struct {
	Slug  string
	Title string
	Date  string
}

// IndexPage renders the post list.
func IndexPage(items []Item, cssPath string) string {
	var b strings.Builder
	b.WriteString("<h1>Tat ca bai viet</h1>\n<ul class=\"post-list\">\n")
	for _, it := range items {
		b.WriteString("<li><a href=\"/blog/" + html.EscapeString(it.Slug) + "\">" +
			html.EscapeString(it.Title) + "</a><br><small>" +
			html.EscapeString(it.Date) + "</small></li>\n")
	}
	b.WriteString("</ul>\n")
	return layout("Tat ca bai viet", b.String(), cssPath)
}

// PostPage renders one announcement with masthead (title, date, tags).
func PostPage(title, date string, tags []string, bodyHTML, cssPath string) string {
	var b strings.Builder
	b.WriteString("<article class=\"prose\">\n<div class=\"masthead\"><h1>" +
		html.EscapeString(title) + "</h1><small>" + html.EscapeString(date) + "</small>")
	if len(tags) > 0 {
		esc := make([]string, 0, len(tags))
		for _, t := range tags {
			esc = append(esc, "<a href=\"/blog/tag/"+html.EscapeString(t)+"\">"+
				html.EscapeString(t)+"</a>")
		}
		b.WriteString("<br><small>" + strings.Join(esc, ", ") + "</small>")
	}
	b.WriteString("</div>\n" + bodyHTML + "</article>\n" +
		"<p><a href=\"/blog/\">&larr; Tat ca bai viet</a></p>")
	return layout(title, b.String(), cssPath)
}

// Package markup renders forum-style inline markdown (bold, italic,
// strikethrough), [text](url) links and > quotes into terminal SGR
// sequences. All code spans and fenced blocks are delegated to codebg so
// code styling has a single source; unknown constructs degrade to their
// raw source text, never lost. Only zero-width SGR/OSC8 sequences are
// added or removed, so display-cell arithmetic in line editors and the
// placeholder erase math (which counts newlines, identical on both render
// paths) are unchanged.
package markup

import (
	"bytes"
	"strings"
	"sync"
	"unicode"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	east "github.com/yuin/goldmark/extension/ast"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/renderer"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"

	"github.com/CleveTok3125/V2V/codebg"
	"github.com/CleveTok3125/V2V/linkify"
)

const (
	sgrBold      = "\x1b[1m"
	sgrBoldOff   = "\x1b[22m"
	sgrItalic    = "\x1b[3m"
	sgrItalicOff = "\x1b[23m"
	sgrStrike    = "\x1b[9m"
	sgrStrikeOff = "\x1b[29m"
	// sgrQuote marks quote lines grey. The closer resets only the
	// foreground (not a full reset) so surrounding colors survive.
	sgrQuote    = "\x1b[90m│ "
	sgrQuoteOff = "\x1b[39m"
)

// Span renders full chat text: code via codebg with syntax highlighting,
// plus trio/link/quote styling. Callers sanitize first, as with codebg.
func Span(text string, st codebg.Style) string {
	return span(text, st, true)
}

// SpanPlain is Span without chroma highlighting, mirroring the
// codebg.Render/codebg.RenderWithStyle split: the sender placeholder
// keeps plain rendering so highlight resets never cancel its grey
// wrapper, and line counts match the highlighted echo either way.
func SpanPlain(text string) string {
	return span(text, codebg.Style{}, false)
}

var (
	mu sync.Mutex
	md = func() goldmark.Markdown {
		r := renderer.NewRenderer(renderer.WithNodeRenderers(util.Prioritized(&sgrRenderer{}, 1000)))
		m := goldmark.New(goldmark.WithRenderer(r))
		// Strikethrough parser only: the Strikethrough Extender would
		// also inject its HTML renderer into ours, so it is wired
		// manually at the same priority the extension uses.
		m.Parser().AddOptions(parser.WithInlineParsers(
			util.Prioritized(extension.NewStrikethroughParser(), 500),
		))
		return m
	}()
)

// span runs the goldmark pipeline, falling back to codebg when the text
// needs no markdown or cannot be parsed. Text containing ESC passes
// through codebg untouched, matching its contract.
func span(text string, st codebg.Style, highlight bool) string {
	render := codebg.Render
	if highlight {
		render = func(s string) string { return codebg.RenderWithStyle(s, st) }
	}
	if text == "" || strings.Contains(text, "\x1b") || !needsMarkup(text) {
		return render(text)
	}
	mu.Lock()
	defer mu.Unlock()
	styleMu.Lock()
	styleMu.style, styleMu.highlight = st, highlight
	styleMu.Unlock()
	var buf bytes.Buffer
	if err := md.Convert([]byte(text), &buf); err != nil {
		return render(text)
	}
	return buf.String()
}

// styleMu carries the per-call style into the shared renderer, guarded
// by mu above.
var styleMu = struct {
	sync.Mutex
	style     codebg.Style
	highlight bool
}{}

// needsMarkup reports whether text plausibly contains trio/link/quote
// markup. Anything it misses renders literal, exactly as codebg alone
// would, so the check only affects cost, never correctness.
func needsMarkup(s string) bool {
	if strings.ContainsAny(s, "`*_[~<") || strings.Contains(s, "](") {
		return true
	}
	for _, line := range strings.Split(s, "\n") {
		if t := strings.TrimLeft(line, " \t"); len(t) > 0 && t[0] == '>' {
			return true
		}
	}
	return false
}

// sgrRenderer renders the supported subset to SGR; everything else falls
// back to raw source bytes.
type sgrRenderer struct{}

func (r *sgrRenderer) RegisterFuncs(reg renderer.NodeRendererFuncRegisterer) {
	for _, k := range []ast.NodeKind{
		ast.KindDocument, ast.KindParagraph, ast.KindText, ast.KindString,
		ast.KindCodeSpan, ast.KindCodeBlock, ast.KindFencedCodeBlock,
		ast.KindBlockquote, ast.KindHeading, ast.KindList, ast.KindListItem,
		ast.KindThematicBreak, ast.KindHTMLBlock,
		ast.KindEmphasis, ast.KindLink, ast.KindAutoLink, ast.KindImage,
		east.KindStrikethrough,
	} {
		reg.Register(k, r.render)
	}
}

func (r *sgrRenderer) render(w util.BufWriter, source []byte, n ast.Node, entering bool) (ast.WalkStatus, error) {
	if !entering {
		return ast.WalkContinue, nil
	}
	_, _ = w.Write([]byte(r.node(source, n)))
	return ast.WalkSkipChildren, nil
}

func (r *sgrRenderer) node(source []byte, n ast.Node) string {
	switch n.Kind() {
	case ast.KindDocument:
		return r.blocks(source, n)
	case ast.KindParagraph:
		return r.inlines(source, n)
	case ast.KindText:
		t := n.(*ast.Text)
		s := string(t.Segment.Value(source))
		if t.SoftLineBreak() || t.HardLineBreak() {
			s += "\n"
		}
		return s
	case ast.KindString:
		return string(n.(*ast.String).Value)
	case ast.KindCodeSpan:
		return codebg.Span(r.codeText(source, n))
	case ast.KindFencedCodeBlock:
		return r.fence(source, n.(*ast.FencedCodeBlock))
	case ast.KindBlockquote:
		return r.quote(source, n)
	case ast.KindEmphasis:
		if n.(*ast.Emphasis).Level >= 2 {
			return sgrBold + r.inlines(source, n) + sgrBoldOff
		}
		return sgrItalic + r.inlines(source, n) + sgrItalicOff
	case east.KindStrikethrough:
		return sgrStrike + r.inlines(source, n) + sgrStrikeOff
	case ast.KindLink:
		return r.link(source, n.(*ast.Link))
	case ast.KindAutoLink:
		return r.autolink(source, n.(*ast.AutoLink))
	case ast.KindImage:
		return r.image(source, n.(*ast.Image))
	default:
		return r.raw(n, source)
	}
}

// image stays literal markdown: chat renders no images. The alt text is
// plain source bytes (never styled), reconstructed around the
// destination, since inline nodes carry no bracket positions.
func (r *sgrRenderer) image(source []byte, n *ast.Image) string {
	var alt strings.Builder
	for c := n.FirstChild(); c != nil; c = c.NextSibling() {
		switch t := c.(type) {
		case *ast.Text:
			alt.Write(t.Segment.Value(source))
		case *ast.String:
			alt.Write(t.Value)
		}
	}
	var b strings.Builder
	b.WriteString("![")
	b.WriteString(alt.String())
	b.WriteString("](")
	b.Write(n.Destination)
	if len(n.Title) > 0 {
		b.WriteString(` "`)
		b.Write(n.Title)
		b.WriteString(`"`)
	}
	b.WriteByte(')')
	return b.String()
}

func (r *sgrRenderer) inlines(source []byte, n ast.Node) string {
	var b strings.Builder
	for c := n.FirstChild(); c != nil; c = c.NextSibling() {
		b.WriteString(r.node(source, c))
	}
	return b.String()
}

// codeText joins a code span's children. Newlines inside the span stay
// newlines (rather than CommonMark spaces) so terminal rows are
// preserved; a lone surrounding space pair is stripped per spec.
func (r *sgrRenderer) codeText(source []byte, n ast.Node) string {
	var b strings.Builder
	for c := n.FirstChild(); c != nil; c = c.NextSibling() {
		switch t := c.(type) {
		case *ast.Text:
			b.Write(t.Segment.Value(source))
			if t.SoftLineBreak() || t.HardLineBreak() {
				b.WriteByte('\n')
			}
		case *ast.String:
			b.Write(t.Value)
		}
	}
	s := b.String()
	if s == " " {
		return ""
	}
	if len(s) >= 2 && s[0] == ' ' && s[len(s)-1] == ' ' {
		s = s[1 : len(s)-1]
	}
	return s
}

// fence replays one fenced block through codebg, so headers, markers and
// highlighting behave exactly as the code-only path.
func (r *sgrRenderer) fence(source []byte, n *ast.FencedCodeBlock) string {
	info := ""
	if n.Info != nil {
		info = strings.TrimSpace(string(n.Info.Segment.Value(source)))
	}
	var code strings.Builder
	lines := n.Lines()
	for i := 0; i < lines.Len(); i++ {
		seg := lines.At(i)
		code.Write(seg.Value(source))
	}
	chunk := "```" + info + "\n" + strings.TrimSuffix(code.String(), "\n") + "\n```"
	styleMu.Lock()
	st, highlight := styleMu.style, styleMu.highlight
	styleMu.Unlock()
	if highlight {
		return codebg.RenderWithStyle(chunk, st)
	}
	return codebg.Render(chunk)
}

// quote renders a blockquote with a grey bar per content line. Blank
// lines stay bare so no trailing spaces are emitted. An empty quote
// falls back to raw source to never drop a row.
func (r *sgrRenderer) quote(source []byte, n ast.Node) string {
	if n.FirstChild() == nil {
		return ">"
	}
	inner := r.blocks(source, n)
	if strings.TrimSpace(inner) == "" {
		return r.raw(n, source)
	}
	lines := strings.Split(inner, "\n")
	for i, l := range lines {
		if l != "" {
			lines[i] = sgrQuote + l + sgrQuoteOff
		}
	}
	return strings.Join(lines, "\n")
}

// link renders [text](url) with linkify styling. Anything but a clean
// http(s) destination stays literal markdown, reconstructed from the
// parsed parts (inline nodes carry no source positions for the brackets).
func (r *sgrRenderer) link(source []byte, n *ast.Link) string {
	dest := string(n.Destination)
	if len(n.Title) == 0 && httpURL(dest) {
		visible := r.inlines(source, n)
		if visible == "" {
			visible = dest
		}
		return linkify.Wrap(dest, visible)
	}
	var b strings.Builder
	b.WriteByte('[')
	b.WriteString(r.inlines(source, n))
	b.WriteString("](")
	b.WriteString(dest)
	if len(n.Title) > 0 {
		b.WriteString(` "`)
		b.Write(n.Title)
		b.WriteString(`"`)
	}
	b.WriteByte(')')
	return b.String()
}

// autolink renders <http(s)://...> the same way. Email autolinks keep
// their brackets, reconstructed around the address.
func (r *sgrRenderer) autolink(source []byte, n *ast.AutoLink) string {
	u := string(n.URL(source))
	if n.AutoLinkType == ast.AutoLinkURL && httpURL(u) {
		return linkify.Wrap(u, u)
	}
	return "<" + string(n.Label(source)) + ">"
}

// blocks joins child blocks preserving source blank lines: the separator
// replays the newlines found between blocks, so rows never shift.
func (r *sgrRenderer) blocks(source []byte, parent ast.Node) string {
	type part struct {
		start, end int
		ok         bool
		gap        string
	}
	var parts []part
	for c := parent.FirstChild(); c != nil; c = c.NextSibling() {
		p := part{}
		p.start, p.end, p.ok = nodeRange(c)
		parts = append(parts, p)
	}
	// Children without positions (marker-only nodes like thematic
	// breaks) interpolate from the surrounding gaps; document bounds
	// anchor the edges since all segments are document offsets.
	for i := range parts {
		if parts[i].ok {
			continue
		}
		lo := 0
		for j := i - 1; j >= 0; j-- {
			if parts[j].ok {
				lo = parts[j].end
				break
			}
		}
		hi := len(source)
		for j := i + 1; j < len(parts); j++ {
			if parts[j].ok {
				hi = parts[j].start
				break
			}
		}
		parts[i].gap = string(source[lo:hi])
	}
	var b strings.Builder
	idx := 0
	for c := parent.FirstChild(); c != nil; c = c.NextSibling() {
		p := &parts[idx]
		idx++
		if idx > 1 {
			sep := "\n"
			if p.ok && parts[idx-2].ok {
				sep = strings.Repeat("\n", lineGap(source, parts[idx-2].end, p.start))
			}
			b.WriteString(sep)
		}
		b.WriteString(r.blockGap(source, c, p.gap))
	}
	return b.String()
}

// block renders one block-level child: supported kinds structurally,
// everything else (lists, headings, HTML, indented code) as raw source.
func (r *sgrRenderer) block(source []byte, n ast.Node) string {
	return r.blockGap(source, n, "")
}

func (r *sgrRenderer) blockGap(source []byte, n ast.Node, gap string) string {
	switch n.Kind() {
	case ast.KindParagraph:
		return r.inlines(source, n)
	case ast.KindFencedCodeBlock:
		return r.fence(source, n.(*ast.FencedCodeBlock))
	case ast.KindBlockquote:
		return r.quote(source, n)
	case ast.KindThematicBreak:
		for _, line := range strings.Split(gap, "\n") {
			if m, ok := thematicLine(line); ok {
				return m
			}
		}
		return strings.TrimSpace(gap)
	default:
		return r.raw(n, source)
	}
}

// thematicLine reports whether a line is a horizontal-rule marker and,
// if so, returns it trimmed. Markers carry no source positions, so the
// rule is re-derived from the surrounding gap text.
func thematicLine(line string) (string, bool) {
	t := strings.Trim(line, " \t")
	if len(t) < 3 {
		return "", false
	}
	mark := t[0]
	if mark != '*' && mark != '-' && mark != '_' {
		return "", false
	}
	for i := 1; i < len(t); i++ {
		if c := t[i]; c != mark && c != ' ' && c != '\t' {
			return "", false
		}
	}
	return t, true
}

// lineGap counts source lines between two byte offsets (at least one),
// so block separators replay blank lines exactly.
func lineGap(source []byte, prevEnd, nextStart int) int {
	if nextStart < prevEnd {
		return 1
	}
	n := 0
	for _, c := range source[prevEnd:nextStart] {
		if c == '\n' {
			n++
		}
	}
	if n < 1 {
		return 1
	}
	return n
}

// nodeRange spans the source bytes covered by a node (markers included),
// so raw fallbacks stay byte-exact.
func nodeRange(n ast.Node) (int, int, bool) {
	lo, hi := -1, -1
	_ = ast.Walk(n, func(m ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		if t, ok := m.(*ast.Text); ok {
			if lo < 0 || t.Segment.Start < lo {
				lo = t.Segment.Start
			}
			if t.Segment.Stop > hi {
				hi = t.Segment.Stop
			}
		}
		// Lines exists on inline nodes too but panics there; blocks only.
		if m.Type() == ast.TypeBlock {
			if b, ok := m.(interface{ Lines() *text.Segments }); ok {
				segs := b.Lines()
				if segs != nil {
					for i := 0; i < segs.Len(); i++ {
						s := segs.At(i)
						if lo < 0 || s.Start < lo {
							lo = s.Start
						}
						if s.Stop > hi {
							hi = s.Stop
						}
					}
				}
			}
		}
		return ast.WalkContinue, nil
	})
	if lo < 0 || hi < lo {
		return 0, 0, false
	}
	return lo, hi, true
}

// raw returns the source lines covered by a node, markers included.
// Unknown constructs degrade to visible literal text, never lost.
func (r *sgrRenderer) raw(n ast.Node, source []byte) string {
	lo, hi, ok := nodeRange(n)
	if !ok {
		return string(n.Text(source))
	}
	for lo > 0 && source[lo-1] != '\n' {
		lo--
	}
	for hi < len(source) && source[hi] != '\n' {
		hi++
	}
	return string(source[lo:hi])
}

// httpURL reports whether u is an absolute http(s) URL without whitespace
// or control bytes, mirroring linkify's scheme requirement.
func httpURL(u string) bool {
	l := strings.ToLower(u)
	var rest string
	if strings.HasPrefix(l, "http://") {
		rest = u[len("http://"):]
	} else if strings.HasPrefix(l, "https://") {
		rest = u[len("https://"):]
	} else {
		return false
	}
	if rest == "" {
		return false
	}
	for _, c := range rest {
		if c == 0x1b || unicode.IsSpace(c) || unicode.IsControl(c) {
			return false
		}
	}
	return true
}

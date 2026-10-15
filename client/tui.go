package main

import (
	"io"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"

	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// Tab indices inside the app are 0-based; the session keeps its own
// 1-based IDs (TabChat/TabSystem).
const (
	tabIndexChat = iota
	tabIndexSystem
)

// inputPlaceholder is the idle draft hint; it is blanked in command mode so a
// bare "/" prompt reads as a command prompt instead of an empty draft.
const inputPlaceholder = "Nhập tin nhắn…"

// uiRefreshMsg tells the app a tab buffer changed and the active tab must
// be pulled again.
type uiRefreshMsg struct{}

// uiPromptMsg carries the input prompt the session wants shown (a reply
// target re-arms it).
type uiPromptMsg struct{ prompt string }

// uiSink is the session's handle on the running app. The session never
// writes the screen itself: it keeps its tab buffers and calls Notify,
// and the app pulls the active tab and repaints.
type uiSink interface {
	Notify()
	SetPrompt(p string)
	Quit()
}

// tuiSink implements uiSink over a Bubble Tea program. Notify is
// coalesced: while a refresh is in flight further changes fold into it,
// so a history burst wakes the app once per frame instead of per line.
type tuiSink struct {
	program *tea.Program
	started atomic.Bool

	mu      sync.Mutex
	pending bool
}

func (k *tuiSink) attach(p *tea.Program) { k.program = p }

// Notify wakes the app. It is safe to call from any goroutine and before
// the program runs: the first render pulls the whole buffer anyway, so a
// refresh dropped before start loses nothing.
func (k *tuiSink) Notify() {
	if !k.started.Load() {
		return
	}
	k.mu.Lock()
	if k.pending {
		k.mu.Unlock()
		return
	}
	k.pending = true
	p := k.program
	k.mu.Unlock()
	if p != nil {
		go p.Send(uiRefreshMsg{})
	}
}

// ack clears the in-flight flag once the app has handled a refresh.
func (k *tuiSink) ack() {
	k.mu.Lock()
	k.pending = false
	k.mu.Unlock()
}

// send delivers a message once the program is running. It never blocks the
// caller: the session calls it while holding DisplayMu, and the event loop
// takes that same lock to render, so a synchronous send could deadlock
// against a render in progress.
func (k *tuiSink) send(m tea.Msg) {
	if !k.started.Load() {
		return
	}
	if pr := k.program; pr != nil {
		go pr.Send(m)
	}
}

func (k *tuiSink) SetPrompt(p string) { k.send(uiPromptMsg{prompt: p}) }

func (k *tuiSink) Quit() {
	if pr := k.program; pr != nil {
		pr.Quit()
	}
}

// tuiTerm adapts the app to the inputTerminal the session loop expects.
// ReadLine yields the next submitted line; the app owns the screen, so
// Refresh and Writer are inert.
type tuiTerm struct {
	lines  chan string
	sink   uiSink
	closed chan struct{}
	once   sync.Once
}

func (t *tuiTerm) ReadLine() (string, error) {
	select {
	case <-t.closed:
		return "", io.EOF
	case line := <-t.lines:
		return line, nil
	}
}

func (t *tuiTerm) SetPrompt(p string) { t.sink.SetPrompt(p) }
func (t *tuiTerm) Refresh()           {}
func (t *tuiTerm) Close()             { t.once.Do(func() { close(t.closed) }) }
func (t *tuiTerm) Writer() io.Writer  { return io.Discard }

// bananaYellow is the system-box border: ANSI 256 index 222 (#ffd75f), close
// to banana yellow (#ffe135).
const bananaYellow = "222"

// commandBorderColor is the input border in slash command mode: ANSI 256
// index 198 (#ff0087), the closest match to #ff2776.
const commandBorderColor = "198"

// footerFg is the status-line foreground: a light grey, brighter than the dim
// chrome so the mode and status stand out (ANSI 250).
const footerFg = "250"

// tuiStyles holds the chrome styles. Colours come from the client config
// theme (ui.theme.accent) instead of being hardcoded, so a themed config is
// honoured; the idle input border and the footer use a neutral grey.
type tuiStyles struct {
	tabActive   lipgloss.Style
	tabInactive lipgloss.Style
	inputEmpty  lipgloss.Style
	inputText   lipgloss.Style
	inputCmd    lipgloss.Style
	footer      lipgloss.Style
	help        lipgloss.Style
	helpTitle   lipgloss.Style
	scrollTrack lipgloss.Style
	scrollThumb lipgloss.Style
	boxChat     lipgloss.Style
	boxLocal    lipgloss.Style
	boxSystem   lipgloss.Style
	boxQuote    lipgloss.Style
	reply       lipgloss.Style
}

// newTUIStyles resolves the chrome styles, falling back to the built-in
// accent when no client config is loaded.
func newTUIStyles() tuiStyles {
	accent := lipgloss.Color("#4f7dff")
	dim := lipgloss.Color("240")
	if ClientCfg != nil {
		if v := strings.TrimSpace(ClientCfg.UI.Theme.Accent); v != "" {
			accent = lipgloss.Color(v)
		}
	}
	rounded := lipgloss.RoundedBorder()
	return tuiStyles{
		tabActive:   lipgloss.NewStyle().Bold(true).Foreground(accent).Padding(0, 1),
		tabInactive: lipgloss.NewStyle().Foreground(dim).Padding(0, 1),
		inputEmpty:  lipgloss.NewStyle().Border(rounded).BorderForeground(dim),
		inputText:   lipgloss.NewStyle().Border(rounded).BorderForeground(accent),
		inputCmd:    lipgloss.NewStyle().Border(rounded).BorderForeground(lipgloss.Color(commandBorderColor)),
		footer:      lipgloss.NewStyle().Foreground(lipgloss.Color(footerFg)),
		help:        lipgloss.NewStyle().Border(rounded).BorderForeground(accent).Padding(1, 2),
		helpTitle:   lipgloss.NewStyle().Bold(true),
		scrollTrack: lipgloss.NewStyle().Foreground(dim),
		scrollThumb: lipgloss.NewStyle().Foreground(accent),
		boxChat:     lipgloss.NewStyle().Foreground(dim),
		boxLocal:    lipgloss.NewStyle().Foreground(lipgloss.Color(commandBorderColor)),
		boxSystem:   lipgloss.NewStyle().Foreground(lipgloss.Color(bananaYellow)),
		boxQuote:    lipgloss.NewStyle().Foreground(dim),
		reply:       lipgloss.NewStyle().Foreground(dim),
	}
}

// Input border modes: grey when the draft is empty, accent blue when it has
// text, and the command colour in slash command mode.
const (
	inputModeEmpty = iota
	inputModeText
	inputModeCommand
)

// app is the Bubble Tea model: a tab bar, the chat viewport and the input
// box. It reads the session buffers on demand and never touches the
// terminal directly.
type app struct {
	sess  *Session
	sink  *tuiSink
	term  *tuiTerm
	lines chan string

	viewport viewport.Model
	input    textarea.Model
	styles   tuiStyles

	hist         *inputHistory
	replyLine    string
	active       int
	width        int
	height       int
	prompt       string
	enhanced     bool
	cmdMode      bool
	dirty        bool
	quitting     bool
	showHelp     bool
	atTopArmed   bool
	pendingOlder bool
	status       string
}

func newApp(sess *Session) *app {
	sink := &tuiSink{}
	lines := make(chan string, 16)
	a := &app{
		sess:     sess,
		sink:     sink,
		lines:    lines,
		active:   tabIndexChat,
		dirty:    true,
		styles:   newTUIStyles(),
		viewport: viewport.New(viewport.WithWidth(80), viewport.WithHeight(10)),
	}
	// FillHeight keeps the viewport exactly viewport.Height() rows, so the
	// scrollbar column always lines up with the chat rows.
	a.viewport.FillHeight = true
	a.term = &tuiTerm{lines: lines, sink: sink, closed: make(chan struct{})}

	ti := textarea.New()
	ti.Placeholder = inputPlaceholder
	ti.ShowLineNumbers = false
	ti.CharLimit = 0
	ti.DynamicHeight = true
	ti.MinHeight = 1
	ti.MaxHeight = 6
	ti.Focus()
	a.input = ti
	// The prompt marker is per line: the first line carries the mode marker
	// and every continuation line gets "...", so a multi-line draft reads as
	// one block. Width 0 disables the component's left padding.
	a.input.SetPromptFunc(0, a.promptFor)
	a.hist = loadHistory(historyFile)
	a.atTopArmed = true
	return a
}

// promptFor renders the textarea prompt. A multi-line draft is the full text
// editor, so it carries no prompt at all; a single-line draft shows the chat
// marker, or the command marker in slash mode.
func (a *app) promptFor(info textarea.PromptInfo) string {
	if a.multiLine() {
		return ""
	}
	if a.cmdMode {
		// A leading space keeps the command aligned with "> " while the
		// slash stays adjacent to the text: " /help".
		return " /"
	}
	return "> "
}

// multiLine reports whether the draft has grown past one line, which is what
// puts the input into full text edit mode.
func (a *app) multiLine() bool {
	return strings.Contains(a.input.Value(), "\n")
}

// inputModeName labels the current input mode for the footer.
func (a *app) inputModeName() string {
	switch {
	case a.multiLine():
		return "soạn nhiều dòng"
	case a.cmdMode:
		return "lệnh"
	default:
		return "chat"
	}
}

// setCmdMode switches command mode and blanks the placeholder while it is on,
// so a bare "/" prompt reads as a command prompt instead of an empty draft.
func (a *app) setCmdMode(on bool) {
	a.cmdMode = on
	if on {
		a.input.Placeholder = ""
	} else {
		a.input.Placeholder = inputPlaceholder
	}
}

func (a *app) Init() tea.Cmd {
	a.sink.started.Store(true)
	return nil
}

func (a *app) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		a.width, a.height = msg.Width, msg.Height
		a.layout()
		a.dirty = true
		return a, nil
	case tea.KeyboardEnhancementsMsg:
		a.enhanced = msg.SupportsKeyDisambiguation()
		return a, nil
	case uiRefreshMsg:
		a.sink.ack()
		a.dirty = true
		return a, nil
	case uiPromptMsg:
		a.prompt = msg.prompt
		return a, nil
	case tea.MouseWheelMsg:
		var cmd tea.Cmd
		a.viewport, cmd = a.viewport.Update(msg)
		a.maybeLoadOlder()
		return a, cmd
	case tea.KeyPressMsg:
		if handled, cmd := a.handleKey(msg); handled {
			return a, cmd
		}
	}
	var cmd tea.Cmd
	a.input, cmd = a.input.Update(msg)
	a.layout()
	return a, cmd
}

// handleKey routes the keys the app owns; anything else falls through to
// the textarea for editing. The help overlay is modal: it swallows keys
// except the ones that close it (and Ctrl+D, which still quits).
func (a *app) handleKey(msg tea.KeyPressMsg) (bool, tea.Cmd) {
	if a.showHelp {
		switch msg.String() {
		case "ctrl+d":
			a.quitting = true
			return true, tea.Quit
		case "esc", "f1", "?", "enter":
			a.showHelp = false
		}
		return true, nil
	}
	// A slash is matched by code, not text: some terminals report the key
	// with an empty Text (or a modifier), and the textarea would drop it.
	// Shift+/ is "?" and must keep opening help.
	if msg.Code == '/' && msg.Text != "?" {
		switch {
		case a.cmdMode && a.input.Value() == "":
			// A second slash on the bare command marker returns to chat mode;
			// both slashes stay as a literal draft.
			a.setCmdMode(false)
			a.input.SetValue("//")
			a.input.CursorEnd()
		case !a.cmdMode && a.input.Value() == "":
			// The first slash switches to command mode: the marker becomes
			// "/" and the draft stays empty.
			a.setCmdMode(true)
		default:
			a.input.InsertRune('/')
		}
		a.layout()
		return true, nil
	}
	switch msg.String() {
	case "ctrl+d":
		a.quitting = true
		return true, tea.Quit
	case "ctrl+c", "esc":
		if a.sess.cancelReply() {
			a.prompt = "| > "
		}
		if a.cmdMode || strings.TrimSpace(a.input.Value()) != "" {
			a.setCmdMode(false)
			a.input.Reset()
			a.layout()
		}
		a.dirty = true
		return true, nil
	case "f1":
		a.showHelp = true
		return true, nil
	case "?":
		if a.input.Value() == "" {
			a.showHelp = true
			return true, nil
		}
	case "pgup", "pgdown":
		var cmd tea.Cmd
		a.viewport, cmd = a.viewport.Update(msg)
		a.maybeLoadOlder()
		return true, cmd
	case "backspace":
		if a.input.Value() == "" {
			if a.sess.cancelReply() {
				a.prompt = "| > "
				a.dirty = true
				return true, nil
			}
			if a.cmdMode {
				// Backspace on the bare command marker leaves command mode.
				a.setCmdMode(false)
				return true, nil
			}
		}
	case "up", "down":
		if a.histNav(msg.String() == "up") {
			return true, nil
		}
	case "tab":
		a.cycleTab(1)
		return true, nil
	case "shift+tab":
		a.cycleTab(-1)
		return true, nil
	case "enter":
		return true, a.submit()
	case "shift+enter":
		if a.enhanced {
			a.insertNewline()
			return true, nil
		}
	case "ctrl+j", "alt+enter":
		a.insertNewline()
		return true, nil
	}
	return false, nil
}

// histNav recalls a history entry. It only applies to a single-line draft:
// a multi-line draft needs the arrows for its own cursor. Reports false
// when the key should fall through to the textarea.
func (a *app) histNav(up bool) bool {
	if a.hist == nil || strings.Contains(a.input.Value(), "\n") {
		return false
	}
	var val string
	var ok bool
	if up {
		val, ok = a.hist.prev(a.input.Value())
	} else {
		val, ok = a.hist.next()
	}
	if !ok {
		return false
	}
	// A recalled command keeps command mode; its stored leading slash is the
	// marker, so it is stripped. A "//..." entry is the literal escape (or a
	// line saved by an older build), so it stays a plain draft instead of
	// turning into "/ /...".
	if strings.HasPrefix(val, "/") && !strings.HasPrefix(val, "//") {
		a.setCmdMode(true)
		a.input.SetValue(val[1:])
	} else {
		a.setCmdMode(false)
		a.input.SetValue(val)
	}
	a.input.CursorEnd()
	a.layout()
	return true
}

// maybeLoadOlder fires one older-history request when the user pages up to
// the top, so scrolling back loads the previous segment automatically.
func (a *app) maybeLoadOlder() {
	if !a.viewport.AtTop() {
		a.atTopArmed = true
		return
	}
	if !a.atTopArmed || !a.sess.olderAvailable() {
		return
	}
	a.atTopArmed = false
	if _, err := a.sess.requestOlder(50); err == nil {
		a.pendingOlder = true
	}
	a.dirty = true
}

// insertNewline grows the draft by one row, since plain Enter submits.
func (a *app) insertNewline() {
	a.input.InsertString("\n")
	a.layout()
}

// submit hands the draft to the session and clears the input box. In command
// mode the marker slash is restored, so the session sees "/cmd".
func (a *app) submit() tea.Cmd {
	text := a.input.Value()
	if a.cmdMode {
		text = "/" + text
	}
	a.setCmdMode(false)
	if a.hist != nil {
		a.hist.add(text)
	}
	a.input.Reset()
	a.layout()
	a.dirty = true
	if strings.TrimSpace(text) == "" || text == "/" {
		return nil
	}
	select {
	case a.lines <- text:
	default:
	}
	return nil
}

func (a *app) switchTab(i int) {
	if i == a.active {
		return
	}
	a.active = i
	a.sess.switchTab(i + 1)
	a.dirty = true
}

// cycleTab moves the active tab by delta, wrapping around so Tab leaves
// the last tab and returns to the first.
func (a *app) cycleTab(delta int) {
	n := len(tabLabels)
	a.switchTab(((a.active+delta)%n + n) % n)
}

// layout fits the viewport between the tab bar and the input box: the
// textarea grows with the draft, so the chat shrinks to match.
func (a *app) layout() {
	if a.width <= 0 || a.height <= 0 {
		return
	}
	inputH := a.input.Height()
	if inputH < 1 {
		inputH = 1
	}
	chrome := 1 + (inputH + 2) + 1 // tabs + bordered input + footer
	if a.replyLine != "" {
		chrome++ // reply preview above the input
	}
	vpH := a.height - chrome
	if vpH < 1 {
		vpH = 1
	}
	a.viewport.SetWidth(a.chatWidth())
	a.viewport.SetHeight(vpH)
	a.input.SetWidth(a.width - 2)
}

// chatWidth is the viewport width: the terminal minus the scrollbar column.
func (a *app) chatWidth() int {
	if a.width <= 1 {
		return 1
	}
	return a.width - 1
}

// scrollbarColumn renders the chat scrollbar as one column: a dim track with
// an accent thumb. Blank while everything fits.
func (a *app) scrollbarColumn() string {
	h := a.viewport.Height()
	if h <= 0 {
		return ""
	}
	rows := make([]string, h)
	total := a.viewport.TotalLineCount()
	if total <= h {
		for i := range rows {
			rows[i] = " "
		}
		return strings.Join(rows, "\n")
	}
	thumb := h * h / total
	if thumb < 1 {
		thumb = 1
	}
	if thumb > h {
		thumb = h
	}
	top := 0
	if span := total - h; span > 0 {
		top = a.viewport.YOffset() * (h - thumb) / span
	}
	for i := range rows {
		if i >= top && i < top+thumb {
			rows[i] = a.styles.scrollThumb.Render("┃")
			continue
		}
		rows[i] = a.styles.scrollTrack.Render("│")
	}
	return strings.Join(rows, "\n")
}

// withScrollbar pads each chat row to the viewport width and appends the
// scrollbar column, so the bar rides the right edge.
func (a *app) withScrollbar(content string) string {
	w := a.chatWidth()
	bar := strings.Split(a.scrollbarColumn(), "\n")
	lines := strings.Split(content, "\n")
	for i := range lines {
		if d := w - ansi.StringWidth(lines[i]); d > 0 {
			lines[i] += strings.Repeat(" ", d)
		}
		if i < len(bar) {
			lines[i] += bar[i]
		}
	}
	return strings.Join(lines, "\n")
}

// refreshContent pulls the active tab and repaints the viewport. The
// session mutates its buffers under DisplayMu, so the entries are copied
// under that lock and rendered outside it. The scroll position is kept
// when the user is reading history; it only follows the bottom when the
// viewport was already there.
// chatContent renders the active buffer for the viewport: every entry as a
// bordered block. A date banner is dropped when its day was already shown (the
// server sends one per window).
func (a *app) chatContent(lines []entry, skipMuted bool) string {
	var sb strings.Builder
	seen := make(map[string]bool, 4)
	for _, e := range lines {
		if skipMuted && e.muted {
			continue
		}
		if e.banner && e.day != "" {
			if seen[e.day] {
				continue
			}
			seen[e.day] = true
		}
		box := a.renderEntryBox(e)
		if box == "" {
			continue
		}
		sb.WriteString(box)
		sb.WriteString(entryGapRow)
	}
	return sb.String()
}

// renderEntryBox draws one entry as a bordered block that hugs its content up
// to the viewport width:
//
//	╭──────────────╮
//	│ header       │
//	├──────────────┤
//	│ ╭──────────╮ │
//	│ │ quote    │ │
//	│ ╰──────────╯ │
//	│ body         │
//	╰──────────────╯
//
// The border colour follows the entry kind. The frame, the divider and the
// nested quote block are drawn here because lipgloss has no border label, mid
// divider, or nested block.
func (a *app) renderEntryBox(e entry) string {
	maxW := a.chatWidth()
	if maxW < 12 {
		maxW = 12
	}
	headerText := e.header
	bodyText := e.body
	quoteText := e.quote

	// Width: the block hugs the widest row, capped at the viewport.
	natural := maxLineWidth(headerText, bodyText)
	if quoteText != "" {
		if qn := maxLineWidth(quoteText) + 4; qn > natural {
			natural = qn
		}
	}
	w := natural + 4
	if w > maxW {
		w = maxW
	}
	if w < 12 {
		w = 12
	}
	inner := w - 4

	headerRows := wrapText(headerText, inner)
	bodyRows := wrapText(bodyText, inner)
	if quoteText != "" {
		quoteW := maxLineWidth(quoteText) + 4
		if quoteW > inner {
			quoteW = inner
		}
		quote := drawBox(nil, wrapText(quoteText, quoteW-4), quoteW, a.styles.boxQuote)
		bodyRows = append(strings.Split(quote, "\n"), bodyRows...)
	}
	if len(headerRows) == 0 && len(bodyRows) == 0 {
		return ""
	}
	border := a.styles.boxChat
	switch e.kind {
	case kindLocal, kindNotice:
		border = a.styles.boxLocal
	case kindSystem:
		border = a.styles.boxSystem
	}
	box := drawBox(headerRows, bodyRows, w, border)
	if e.rightAligned() && maxW > w {
		box = padLeftLines(box, maxW-w)
	}
	return box
}

// padLeftLines indents every line by n spaces, right-aligning a block.
func padLeftLines(s string, n int) string {
	if n <= 0 {
		return s
	}
	pad := strings.Repeat(" ", n)
	lines := strings.Split(s, "\n")
	for i := range lines {
		lines[i] = pad + lines[i]
	}
	return strings.Join(lines, "\n")
}

// maxLineWidth is the widest display row in s (ANSI-aware).
func maxLineWidth(parts ...string) int {
	w := 0
	for _, part := range parts {
		for _, line := range strings.Split(part, "\n") {
			if d := ansi.StringWidth(line); d > w {
				w = d
			}
		}
	}
	return w
}

// wrapText word-wraps s to width (ANSI-aware), one row per line.
func wrapText(s string, width int) []string {
	if s == "" {
		return nil
	}
	if width < 1 {
		width = 1
	}
	return strings.Split(ansi.Wrap(s, width, " "), "\n")
}

// drawBox frames the header rows, a divider, then the body rows, at width.
func drawBox(headerRows, bodyRows []string, width int, border lipgloss.Style) string {
	inner := width - 4
	if inner < 1 {
		inner = 1
	}
	side := border.Render("│")
	row := func(s string) string {
		if pad := inner - ansi.StringWidth(s); pad > 0 {
			s += strings.Repeat(" ", pad)
		}
		return side + " " + s + " " + side
	}
	rule := width - 2
	if rule < 0 {
		rule = 0
	}
	bar := func(left, right string) string {
		return border.Render(left + strings.Repeat("─", rule) + right)
	}
	out := make([]string, 0, len(headerRows)+len(bodyRows)+4)
	out = append(out, bar("╭", "╮"))
	for _, r := range headerRows {
		out = append(out, row(r))
	}
	if len(headerRows) > 0 && len(bodyRows) > 0 {
		out = append(out, bar("├", "┤"))
	}
	for _, r := range bodyRows {
		out = append(out, row(r))
	}
	out = append(out, bar("╰", "╯"))
	return strings.Join(out, "\n")
}

// stripGutter removes the "|" line gutter (plus the up-to-three spaces that
// follow it) and an input marker ("> " or "...") when present, on every row.
// The box supplies its own inset, and a multi-row header (an input echo, a
// heredoc frame) must not keep a stray "|" on its later rows.
func stripGutter(s string) string {
	if s == "" {
		return ""
	}
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		lines[i] = stripGutterLine(line)
	}
	return strings.Join(lines, "\n")
}

// leadANSIRe matches the escape sequences a row may start with (SGR or OSC8),
// so the gutter behind them is still found: the placeholder and a pending quote
// arrive dim-wrapped.
var leadANSIRe = regexp.MustCompile(`^(?:\x1b\[[0-9;]*m|\x1b\]8;;[^\x1b]*\x1b\\)+`)

func stripGutterLine(line string) string {
	prefix := leadANSIRe.FindString(line)
	line = line[len(prefix):]
	line = strings.TrimPrefix(line, "|")
	for i := 0; i < 3 && strings.HasPrefix(line, " "); i++ {
		line = line[1:]
	}
	line = strings.TrimPrefix(line, "> ")
	line = strings.TrimPrefix(line, "... ")
	return prefix + line
}

func (a *app) refreshContent() {
	if !a.dirty {
		return
	}
	oldOffset := a.viewport.YOffset()
	atBottom := a.viewport.AtBottom()

	a.sess.Display.DisplayMu.Lock()
	a.active = a.sess.Display.ActiveTab - 1
	var lines []entry
	if a.active == tabIndexSystem {
		lines = append(lines, a.sess.Display.TabSys.lines...)
	} else {
		lines = append(lines, a.sess.Display.TabChat.lines...)
	}
	a.sess.Display.DisplayMu.Unlock()

	prevLines := a.viewport.TotalLineCount()
	content := a.chatContent(lines, a.active == tabIndexChat)
	a.viewport.SetContent(content)
	newLines := a.viewport.TotalLineCount()

	switch {
	case a.pendingOlder:
		// Older history was prepended: move down by the inserted lines so
		// the text under the cursor stays put.
		a.pendingOlder = false
		a.viewport.SetYOffset(oldOffset + (newLines - prevLines))
	case atBottom:
		a.viewport.GotoBottom()
	default:
		a.viewport.SetYOffset(oldOffset)
	}
	a.status = a.sess.uiStatus()
	a.dirty = false
}

func (a *app) renderTabs() string {
	labels := []string{"1:chat", "2:system"}
	parts := make([]string, 0, len(labels))
	for i, l := range labels {
		if i == a.active {
			parts = append(parts, a.styles.tabActive.Render(l))
			continue
		}
		parts = append(parts, a.styles.tabInactive.Render(l))
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, parts...)
}

// inputMode picks the input border colour from the draft state.
func (a *app) inputMode() int {
	switch {
	case a.cmdMode:
		return inputModeCommand
	case a.input.Value() != "":
		return inputModeText
	default:
		return inputModeEmpty
	}
}

// inputStyle is the input box style for the current mode.
func (a *app) inputStyle() lipgloss.Style {
	switch a.inputMode() {
	case inputModeCommand:
		return a.styles.inputCmd
	case inputModeText:
		return a.styles.inputText
	default:
		return a.styles.inputEmpty
	}
}

// renderInputArea is the input box. A pending reply rides as a bordered row on
// top of it, sharing the frame, so the two read as one control.
func (a *app) renderInputArea() string {
	st := a.inputStyle()
	if a.replyLine == "" {
		return st.Render(a.input.View())
	}
	head := st.Border(replyBorder(), true, true, true, true).Width(a.width).
		Render(a.styles.reply.Render(" " + a.replyLine))
	body := st.Border(lipgloss.RoundedBorder(), false, true, true, true).Width(a.width).
		Render(a.input.View())
	return head + "\n" + body
}

// replyBorder is the rounded border with a T-junction footer, so the reply row
// reads as attached to the input box below it.
func replyBorder() lipgloss.Border {
	b := lipgloss.RoundedBorder()
	b.BottomLeft = "├"
	b.BottomRight = "┤"
	return b
}

// renderFooter shows transient state (a background-load status or the reply
// prompt) plus the current input mode, with a pointer at the help overlay when
// nothing else is pending.
func (a *app) renderFooter() string {
	line := a.status
	if line == "" && a.prompt != "" && a.prompt != "| > " {
		line = strings.TrimSpace(a.prompt)
	}
	mode := a.inputModeName()
	if line == "" {
		line = mode + " • ? trợ giúp"
	} else {
		line = line + " • " + mode
	}
	return a.styles.footer.MaxWidth(a.width).Render(line)
}

// helpView is the keybinding overlay, centred over the chat area.
func (a *app) helpView() string {
	lines := []string{
		a.styles.helpTitle.Render("Phím tắt"),
		"",
		"Tab / Shift+Tab     đổi tab (1:chat, 2:system)",
		"Enter               gửi tin nhắn",
		"Shift+Enter         xuống dòng (cần terminal hỗ trợ)",
		"Alt+Enter / Ctrl+J  xuống dòng (dự phòng)",
		"↑ / ↓               lịch sử nhập (khi nháp một dòng)",
		"PgUp / PgDn         cuộn chat; tới đỉnh để tải thêm",
		"Lăn chuột           cuộn chat",
		"Shift + kéo chuột   chọn (bôi đen) văn bản",
		"Ctrl+C              xóa nháp",
		"Ctrl+D              thoát",
		"? / F1              đóng trợ giúp",
		"Esc                 đóng trợ giúp",
	}
	box := a.styles.help.Render(strings.Join(lines, "\n"))
	return lipgloss.Place(a.width, a.viewport.Height(), lipgloss.Center, lipgloss.Center, box)
}

func (a *app) View() tea.View {
	if a.quitting {
		return tea.NewView("")
	}
	a.syncReplyLine()
	a.refreshContent()
	chat := a.withScrollbar(a.viewport.View())
	if a.showHelp {
		chat = a.helpView()
	}
	parts := []string{a.renderTabs(), chat}
	parts = append(parts, a.renderInputArea(), a.renderFooter())
	v := tea.NewView(strings.Join(parts, "\n"))
	// Capture the mouse so the wheel scrolls the chat; clicks are ignored.
	v.MouseMode = tea.MouseModeCellMotion
	return v
}

// syncReplyLine refreshes the reply preview shown above the input box and
// re-lays out when it appears or disappears, so the chat keeps its height.
func (a *app) syncReplyLine() {
	a.sess.Display.DisplayMu.Lock()
	line := a.sess.replyPreviewLine()
	a.sess.Display.DisplayMu.Unlock()
	if line == a.replyLine {
		return
	}
	a.replyLine = line
	a.layout()
}

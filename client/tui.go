package main

import (
	"io"
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

// commandBorderColor is the input border in slash command mode: ANSI 256
// index 198 (#ff0087), the closest match to #ff2776.
const commandBorderColor = "198"

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
		footer:      lipgloss.NewStyle().Foreground(dim),
		help:        lipgloss.NewStyle().Border(rounded).BorderForeground(accent).Padding(1, 2),
		helpTitle:   lipgloss.NewStyle().Bold(true),
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

// promptFor renders the textarea prompt: the mode marker on the first line,
// a continuation marker on the rest. Command mode shows "/" like neovim's
// ":" prompt; the slash is not part of the draft, it is restored on submit.
func (a *app) promptFor(info textarea.PromptInfo) string {
	if info.LineNumber > 0 {
		return "..."
	}
	if a.cmdMode {
		// A leading space keeps the command aligned with "> " while the
		// slash stays adjacent to the text: " /help".
		return " /"
	}
	return "> "
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
	case "ctrl+c":
		if a.cmdMode || strings.TrimSpace(a.input.Value()) != "" {
			a.setCmdMode(false)
			a.input.Reset()
			a.layout()
		}
		return true, nil
	case "esc":
		if a.cmdMode || strings.TrimSpace(a.input.Value()) != "" {
			a.setCmdMode(false)
			a.input.Reset()
			a.layout()
		}
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
		if a.cmdMode && a.input.Value() == "" {
			// Backspace on the bare command marker leaves command mode.
			a.setCmdMode(false)
			return true, nil
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
	vpH := a.height - chrome
	if vpH < 1 {
		vpH = 1
	}
	a.viewport.SetWidth(a.width)
	a.viewport.SetHeight(vpH)
	a.input.SetWidth(a.width - 2)
}

// refreshContent pulls the active tab and repaints the viewport. The
// session mutates its buffers under DisplayMu, so the entries are copied
// under that lock and rendered outside it. The scroll position is kept
// when the user is reading history; it only follows the bottom when the
// viewport was already there.
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

	var sb strings.Builder
	for _, e := range lines {
		sb.WriteString(e.text())
	}
	prevLines := a.viewport.TotalLineCount()
	// Wrap at word boundaries (ANSI-aware) before handing the text to the
	// viewport, whose own SoftWrap would break mid-word.
	content := sb.String()
	if a.width > 0 {
		content = ansi.Wrap(content, a.width, " ")
	}
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

func (a *app) renderInput() string {
	switch a.inputMode() {
	case inputModeCommand:
		return a.styles.inputCmd.Render(a.input.View())
	case inputModeText:
		return a.styles.inputText.Render(a.input.View())
	default:
		return a.styles.inputEmpty.Render(a.input.View())
	}
}

// renderFooter shows transient state: a background-load status, or the reply
// prompt; when idle it points at the help overlay. The key list itself lives
// in that overlay.
func (a *app) renderFooter() string {
	line := a.status
	if line == "" && a.prompt != "" && a.prompt != "| > " {
		line = strings.TrimSpace(a.prompt)
	}
	if line == "" {
		line = "? trợ giúp"
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
	a.refreshContent()
	chat := a.viewport.View()
	if a.showHelp {
		chat = a.helpView()
	}
	v := tea.NewView(strings.Join([]string{
		a.renderTabs(),
		chat,
		a.renderInput(),
		a.renderFooter(),
	}, "\n"))
	// Capture the mouse so the wheel scrolls the chat; clicks are ignored.
	v.MouseMode = tea.MouseModeCellMotion
	return v
}
